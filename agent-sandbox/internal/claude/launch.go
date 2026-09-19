// Package claude builds and runs the sandboxed `claude` command: it parses the
// launcher's arguments, constructs the `nono wrap … claude …` invocation
// (including the hook settings injected in hook mode), and executes it.
package claude

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/agentconfig"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/gitutil"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/policysnapshot"
)

// agentName identifies the launched agent for host-policy resolution. Only
// "claude" exists today; new agents pass their own identifier.
const agentName = "claude"

// Options carries the claude passthrough options (everything after "--").
// agent-sandbox no longer forwards options to nono.
type Options struct {
	ClaudeOpts []string
	EnvRefs    []string
}

// ParseArgs splits the raw args into the config-file path and the claude
// passthrough options. The first standalone "--" separates agent-sandbox's own
// region (before) from claude options (after). Only "--config <val>" /
// "--config=<val>" and "--env <ref>" / "--env=<ref>"
// are accepted before "--"; any other pre-"--" token is an
// error, because agent-sandbox no longer forwards options to nono (the sandbox
// profile is configured via [agents.<name>].profile in agent-sandbox.toml).
// defaultConfig is used when no "--config" is given.
func ParseArgs(args []string, defaultConfig string) (string, Options, error) {
	configFile := defaultConfig
	var opts Options

	pre := args
	for i, a := range args {
		if a == "--" {
			pre = args[:i]
			opts.ClaudeOpts = args[i+1:]
			break
		}
	}

	for i := 0; i < len(pre); i++ {
		a := pre[i]
		switch {
		case a == "--config":
			if i+1 < len(pre) {
				configFile = pre[i+1]
				i++
			}
		case strings.HasPrefix(a, "--config="):
			configFile = strings.TrimPrefix(a, "--config=")
		case a == "--env":
			if i+1 < len(pre) {
				opts.EnvRefs = append(opts.EnvRefs, pre[i+1])
				i++
			}
		case strings.HasPrefix(a, "--env="):
			opts.EnvRefs = append(opts.EnvRefs, strings.TrimPrefix(a, "--env="))
		case a == "--profile" || a == "-p" || strings.HasPrefix(a, "--profile="):
			return "", Options{}, fmt.Errorf("--profile is no longer accepted; configure the sandbox profile via [agents.<name>].profile in agent-sandbox.toml")
		default:
			return "", Options{}, fmt.Errorf("unexpected option %q before \"--\": agent-sandbox no longer forwards options to nono; only --config and --env are accepted before \"--\", and claude options go after \"--\"", a)
		}
	}
	return configFile, opts, nil
}

// ValidatePassthrough rejects the one claude passthrough option agent-sandbox
// reserves for itself: --settings, which carries the PreToolUse hook that
// routes every command through the broker. --mcp-config and
// --strict-mcp-config used to be reserved too, while agent-sandbox generated
// an MCP config of its own; it no longer generates one, so they pass through.
func ValidatePassthrough(claudeOpts []string) error {
	for _, arg := range claudeOpts {
		if strings.HasPrefix(arg, "--settings") {
			return fmt.Errorf("--settings is not allowed")
		}
	}
	return nil
}

// executablePath resolves the launcher's own binary. It is a variable rather
// than a direct os.Executable call only so tests can pin it; nothing outside
// this package sets it.
var executablePath = os.Executable

// launcherPath is the absolute path of this binary, symlinks resolved —
// Landlock resolves them too, so a grant has to name the target rather than the
// link.
func launcherPath() (string, error) {
	self, err := executablePath()
	if err != nil {
		return "", fmt.Errorf("locate agent-sandbox: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
		return resolved, nil
	}
	return self, nil
}

// hookProbePayload is a minimal PreToolUse payload; hookProbeWant is what the
// hook must rewrite it into. The command is `true` so that nothing runs even if
// the response were somehow acted on.
const (
	hookProbePayload = `{"tool_name":"Bash","tool_input":{"command":"true"}}`
	hookProbeWant    = "agent-sandbox exec --"
)

// probeHook runs the PreToolUse hook inside the agent's own sandbox and checks
// that it rewrites a command, before the agent is launched.
//
// It exists because the failure it catches is invisible and unsafe. Claude Code
// blocks a tool call only when a hook exits 2; a hook that cannot start at all
// is a non-blocking error, and the original command then runs unwrapped, in the
// agent's own sandbox, under none of the command profile's limits. Nothing in
// the session says so. The launcher therefore proves the hook runs under this
// exact profile rather than assuming it, in the same "measure, do not parse"
// shape doctor uses for the broker socket variable.
//
// --allow-cwd is required because nono refuses working-directory access in
// non-interactive mode, which is how this runs.
func probeHook(profilePath, self string) error {
	nonoPath, err := exec.LookPath("nono")
	if err != nil {
		return fmt.Errorf("nono not found in PATH: %w", err)
	}
	cmd := exec.Command(nonoPath, "wrap", "--silent", "--allow-cwd",
		"--profile", profilePath, "--read-file", self, "--", self, "hook")
	cmd.Stdin = strings.NewReader(hookProbePayload)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return fmt.Errorf("the PreToolUse hook cannot run under %s: %v: %s",
			profilePath, runErr, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), hookProbeWant) {
		return fmt.Errorf("the PreToolUse hook ran under %s but did not route the command "+
			"through the broker: %s", profilePath, strings.TrimSpace(string(out)))
	}
	return nil
}

// BuildArgs constructs the nono executable path and the argv used to launch
// Claude under the sandbox for cfg. It injects the operator's profile at
// profilePath via `--profile` (no user nono options are forwarded) and, in
// hook mode, injects the PreToolUse hook via `claude --settings`; otherwise it
// disables the Bash and Monitor tools. The injected settings carry the hook
// only; the profile contributes nothing to them.
func BuildArgs(cfg *config.Config, opts Options,
	profilePath, brokerSocket, shellWrapper string) (string, []string, error) {
	nonoPath, err := exec.LookPath("nono")
	if err != nil {
		return "", nil, fmt.Errorf("nono not found in PATH: %w", err)
	}
	args := []string{"nono", "wrap"}

	// The agent runs `agent-sandbox hook` (hook mode) and `agent-sandbox serve`
	// (mcp mode) as its own direct children — inside its own sandbox, not
	// through the broker — so the launcher's binary has to be reachable from
	// the agent profile. Without it nono refuses the execve and every command
	// fails with nothing on screen to explain it.
	//
	// It is a flag rather than a line in the hand-written profile because only
	// the launcher knows where it lives: on a mise-managed toolchain the path
	// carries the Go version, so an upgrade renumbers it and a profile entry
	// would silently stop matching. A read grant carries the execute right.
	self, selfErr := launcherPath()
	if selfErr != nil {
		return "", nil, selfErr
	}
	args = append(args, "--read-file", self)

	// The agent's shell is a generated wrapper (see shellwrapper.go), reached
	// the same way and for the same reason as the launcher's own binary: only
	// the launcher knows where it wrote it, and without the read grant nono
	// refuses the execve, Claude falls back to the host's bash, and every tool
	// result is prefixed with a line about an unreadable ~/.bashrc.
	if shellWrapper != "" {
		args = append(args, "--read-file", shellWrapper)
	}

	cwd, cwdErr := os.Getwd()
	if cwdErr == nil {
		if mainGit, ok := gitutil.DetectWorktreeGitDir(cwd); ok {
			args = append(args, "--allow", mainGit)
		}
	}

	if brokerSocket != "" {
		args = append(args, "--allow-unix-socket", brokerSocket)
	}
	if profilePath != "" {
		args = append(args, "--profile", profilePath)
	}

	args = append(args, "claude")
	args = append(args, "--append-system-prompt", agentconfig.Pointer())

	settingsStr, err := settingsJSON(cfg.ToolMode == "hook")
	if err != nil {
		return "", nil, err
	}
	if settingsStr != "" {
		args = append(args, "--settings", settingsStr)
	}
	if cfg.ToolMode != "hook" {
		args = append(args, "--disallowed-tools", "Bash,Monitor")
	}

	args = append(args, opts.ClaudeOpts...)
	return nonoPath, args, nil
}

// runDeps holds the launcher's collaborators so run can be tested without
// touching the command broker, the real process, or os.Exit.
type runDeps struct {
	// agentProfile resolves — and existence-checks — the nono profile the
	// launched agent runs under. It stays a dependency so tests can drive run
	// without touching the filesystem.
	agentProfile func(*config.Config) (string, error)
	// verifyHook proves the PreToolUse hook can actually run under the agent's
	// profile. Hook mode only; mcp mode injects no hook.
	verifyHook  func(profilePath, selfPath string) error
	startBroker func(*config.Config) (socket string, cleanup func(), err error)
	// startShellWrapper writes the shell Claude runs tool commands with. It
	// returns the wrapper's path and a cleanup that removes it.
	startShellWrapper func() (path string, cleanup func(), err error)
	supervise         func(path string, args []string) int
	exit              func(code int)
}

// defaultAgentProfile resolves the launched agent's profile path from cfg and
// fails when it is not on disk. Nothing here opens the file: nono reads it at
// launch, and doctor asks nono to validate it. agent-sandbox only names it.
func defaultAgentProfile(c *config.Config) (string, error) {
	path := c.AgentProfilePath(agentName)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("%w: %s", config.ErrAgentProfileMissing, path)
	}
	return path, nil
}

// Run generates the sandbox profile, starts the command broker, launches
// Claude under nono, and tears the broker down when Claude exits. It replaces
// the old syscall.Exec approach so the launcher can outlive Claude and run
// teardown.
func Run(cfg *config.Config, opts Options) error {
	return run(cfg, opts, defaultDeps())
}

// defaultDeps is the real collaborator set, separate from Run so a test can
// assert every field is wired. A dependency left nil here is not a test
// failure, it is a panic at launch — run calls each one unconditionally for
// the mode it applies to, and only Run builds this set.
func defaultDeps() runDeps {
	return runDeps{
		agentProfile:      defaultAgentProfile,
		verifyHook:        probeHook,
		startBroker:       startCommandBroker,
		startShellWrapper: defaultShellWrapper,
		supervise:         superviseProcess,
		exit:              os.Exit,
	}
}

func run(cfg *config.Config, opts Options, d runDeps) error {
	profilePath, err := d.agentProfile(cfg)
	if err != nil {
		return err
	}

	if cfg.ToolMode == "hook" {
		self, selfErr := launcherPath()
		if selfErr != nil {
			return selfErr
		}
		if err := d.verifyHook(profilePath, self); err != nil {
			return fmt.Errorf("hook check: %w", err)
		}
	}

	// A wrapper that cannot be written costs noise, not safety: Claude falls
	// back to the host's bash, which sources ~/.bashrc — a file the agent
	// profile no longer grants — and prefixes every tool result with a line
	// saying so. Report it and launch anyway.
	shellWrapper, cleanupWrapper, werr := d.startShellWrapper()
	if werr != nil {
		fmt.Fprintf(os.Stderr, "agent-sandbox: %v\n"+
			"agent-sandbox: Claude will use the host's shell; expect a ~/.bashrc line "+
			"on every tool result\n", werr)
		shellWrapper = ""
	}
	defer func() {
		if cleanupWrapper != nil {
			cleanupWrapper()
		}
	}()

	brokerSocket, cleanupBroker, err := d.startBroker(cfg)
	if err != nil {
		return fmt.Errorf("command broker: %w", err)
	}
	defer func() {
		if cleanupBroker != nil {
			cleanupBroker()
		}
	}()

	nonoPath, nonoArgs, err := BuildArgs(cfg, opts, profilePath, brokerSocket, shellWrapper)
	if err != nil {
		return err
	}

	// superviseProcess inherits the launcher's environment, so setting it here
	// is the simplest correct way to hand the broker socket path to the child.
	os.Setenv(broker.SocketEnvVar, brokerSocket)
	if shellWrapper != "" {
		os.Setenv(ShellEnvVar, shellWrapper)
	}

	code := d.supervise(nonoPath, nonoArgs)

	if cleanupBroker != nil {
		cleanupBroker()
		cleanupBroker = nil
	}
	if cleanupWrapper != nil {
		cleanupWrapper()
		cleanupWrapper = nil
	}
	d.exit(code)
	return nil
}

// startCommandBroker launches the broker in its own nono session and returns
// the socket path plus a cleanup that stops it.
//
// The broker no longer runs in this process. It runs inside a sandbox whose
// command policies govern everything it executes, which is the whole point: a
// broker outside the sandbox would execute commands with the launcher's own
// reach.
func startCommandBroker(cfg *config.Config) (string, func(), error) {
	nonoPath, err := exec.LookPath("nono")
	if err != nil {
		return "", nil, fmt.Errorf("nono not found in PATH: %w", err)
	}
	selfPath, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("locate agent-sandbox: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, fmt.Errorf("getwd: %w", err)
	}
	sockPath, err := BrokerSocketPath()
	if err != nil {
		return "", nil, err
	}
	// A socket left by a killed run would make the broker's bind fail forever.
	if rmErr := os.Remove(sockPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		return "", nil, fmt.Errorf("remove stale socket: %w", rmErr)
	}

	args := BrokerArgs(cfg, nonoPath, selfPath, sockPath, cwd)
	cmd := exec.Command(nonoPath, args[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("start command broker: %w", err)
	}

	// waitExited carries cmd.Wait's result exactly once. Starting it here,
	// before the readiness race below, is what lets that race observe the
	// child dying (nono rejecting the profile, say) instead of only ever
	// seeing the socket never appear; the channel is buffered so whichever
	// side — the race below, or cleanup later — ends up reading it does not
	// block a send from the other.
	waitExited := make(chan error, 1)
	go func() { waitExited <- cmd.Wait() }()

	childExited, err := waitForSocketOrExit(sockPath, brokerStartTimeout, waitExited)
	if err != nil {
		if !childExited {
			// Still running past the deadline, or wedged mid-startup: stop it and
			// reap it before reporting, so this call never leaves an orphaned
			// broker process behind.
			cmd.Process.Kill()
			<-waitExited
		}
		os.Remove(sockPath)
		return "", nil, err
	}

	cleanup := func() {
		if cmd.Process == nil {
			os.Remove(sockPath)
			return
		}
		if sigErr := cmd.Process.Signal(syscall.SIGTERM); sigErr != nil {
			fmt.Fprintf(os.Stderr, "agent-sandbox: signal command broker: %v\n", sigErr)
		}
		// Nothing here can confirm that nono forwards SIGTERM into the session
		// it supervises, so this cannot simply wait on cmd.Wait() forever: a
		// broker that never receives (or never acts on) the signal would hang
		// agent-sandbox claude after the agent has already exited, with nothing
		// on screen explaining why. Escalate to SIGKILL instead once
		// brokerStopTimeout passes.
		select {
		case waitErr := <-waitExited:
			if waitErr != nil {
				fmt.Fprintf(os.Stderr, "agent-sandbox: command broker: %v\n", waitErr)
			}
		case <-time.After(brokerStopTimeout):
			fmt.Fprintf(os.Stderr,
				"agent-sandbox: command broker did not exit within %s after SIGTERM; killing it\n",
				brokerStopTimeout)
			if killErr := cmd.Process.Kill(); killErr != nil {
				fmt.Fprintf(os.Stderr, "agent-sandbox: kill command broker: %v\n", killErr)
			}
			<-waitExited
		}
		os.Remove(sockPath)
	}
	return sockPath, cleanup, nil
}

// brokerStartTimeout bounds how long the launcher waits for the broker's socket
// to appear. A sandbox that cannot start fails here rather than leaving Claude
// running against a broker that will never answer.
const brokerStartTimeout = 15 * time.Second

// brokerStopTimeout bounds how long teardown waits for the broker to exit
// after SIGTERM before escalating to SIGKILL. Signal forwarding into a nono
// session is not this package's to verify, so teardown cannot simply trust it
// and wait forever.
//
// It is a var, not a const, so a test can shrink it rather than spend several
// real seconds proving the escalation path actually fires.
var brokerStopTimeout = 5 * time.Second

// waitForSocketOrExit waits for the broker's socket to appear, racing that
// against the child exiting first via exited. Polling for the socket (rather
// than a readiness handshake) is necessary because the broker is behind a
// sandbox boundary: there is no shared channel to signal on until the socket
// itself appears. Racing it against exited is what turns "nono rejected the
// profile and the child exited within milliseconds" into an immediate,
// specific error instead of a silent wait for the full timeout.
//
// childExited reports whether exited fired (in which case the child is
// already reaped and err explains why it died) or the deadline passed with
// the child still running (in which case the caller still owns stopping and
// reaping it).
func waitForSocketOrExit(path string, timeout time.Duration, exited <-chan error) (childExited bool, err error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(path); statErr == nil {
			return false, nil
		}
		select {
		case waitErr := <-exited:
			if waitErr == nil {
				// A nil error from cmd.Wait means the process exited 0: unusual for
				// nono to do without ever binding the socket, but still reported as
				// a specific, immediate failure rather than folded into the
				// generic timeout message below.
				return true, fmt.Errorf("command broker exited (status 0) before binding its socket")
			}
			return true, fmt.Errorf("command broker exited before binding its socket: %w", waitErr)
		case <-time.After(25 * time.Millisecond):
		}
	}
	return false, fmt.Errorf("command broker did not start within %s; run `agent-sandbox doctor`", timeout)
}

// BrokerArgs builds the `nono run` argv for the command broker's session.
//
// The broker is a sibling of the agent's sandbox, not a child of it: nono
// refuses to nest, and the broker must be the session entrypoint so the profile
// applies to everything it executes. It is deliberately not given --allow-cwd;
// the working directory reaches the profile through --workdir, which is what
// $WORKDIR expands to inside it.
//
// Everything the broker needs in order to *start* is granted here, on the
// command line, rather than being left to the operator's profile. --read-file
// covers its own binary (a read grant carries the execute right), and
// --allow-unix-socket-bind covers the socket. Measured: under a profile that
// grants neither, an absolute-path invocation of this binary exits 127 with no
// output; adding --read-file alone makes the same invocation run. Keeping these
// on the launcher's side means an operator narrowing what *commands* may reach
// cannot accidentally stop the broker from starting, and it removes the
// profile's grants from the set of things that decide whether a session comes
// up at all.
//
// The entrypoint is invoked by its absolute path. It was invoked by base name
// while the profile carried command_policies, because tool-sandbox treats an
// absolute-path invocation of a policy-controlled command as a direct exec
// bypass and refuses it. With no command_policies left, that constraint is gone
// and only base-name resolution's own hazard would remain: nono resolves the
// bare name through this launcher's PATH, so a different, stale copy earlier on
// PATH would silently become the broker instead of this one.
func BrokerArgs(cfg *config.Config, nonoPath, selfPath, sockPath, workdir string) []string {
	return []string{
		nonoPath, "run", "--silent",
		"--profile", cfg.CommandProfilePath(),
		"--workdir", workdir,
		"--read-file", selfPath,
		"--allow-unix-socket-bind", sockPath,
		"--",
		selfPath, "broker", "--socket", sockPath,
	}
}

// BrokerSocketPath returns a per-process socket path under
// policysnapshot.StateDir(). It stays short on purpose: unix socket paths are
// limited to about 104 bytes on macOS.
//
// It is exported so `agent-sandbox debug` can print the same
// `--allow-unix-socket` grant the launcher builds.
func BrokerSocketPath() (string, error) {
	dir, err := policysnapshot.StateDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create state dir: %w", err)
	}
	return filepath.Join(dir, fmt.Sprintf("broker-%d.sock", os.Getpid())), nil
}
