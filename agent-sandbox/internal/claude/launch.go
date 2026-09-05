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
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxhost"
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
// profile is configured in [sandbox.host]). defaultConfig is used when no
// "--config" is given.
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
			return "", Options{}, fmt.Errorf("--profile is no longer accepted; configure the sandbox profile in [sandbox.host] of agent-sandbox.toml")
		default:
			return "", Options{}, fmt.Errorf("unexpected option %q before \"--\": agent-sandbox no longer forwards options to nono; only --config and --env are accepted before \"--\", and claude options go after \"--\"", a)
		}
	}
	return configFile, opts, nil
}

// ValidatePassthrough rejects claude passthrough options that agent-sandbox
// reserves for itself: --settings always, and --mcp-config / --strict-mcp-config
// when the built-in GitHub MCP config is enabled.
func ValidatePassthrough(claudeOpts []string, githubMCPEnabled bool) error {
	for _, arg := range claudeOpts {
		if strings.HasPrefix(arg, "--settings") {
			return fmt.Errorf("--settings is not allowed")
		}
		if githubMCPEnabled &&
			(strings.HasPrefix(arg, "--mcp-config") || strings.HasPrefix(arg, "--strict-mcp-config")) {
			return fmt.Errorf("%s is not allowed when the GitHub MCP is enabled (GITHUB_MCP_TOKEN set)", arg)
		}
	}
	return nil
}

// BuildArgs constructs the nono executable path and the argv used to launch
// Claude under the sandbox for cfg. It injects the generated profile at
// profilePath via `--profile` (no user nono options are forwarded) and, in
// hook mode, injects the PreToolUse hook via `claude --settings`; otherwise it
// disables the Bash and Monitor tools. denyRules are folded into the injected
// settings as additional capability denies.
func BuildArgs(cfg *config.Config, opts Options, mcpConfigPath,
	profilePath string, denyRules []string, brokerSocket string) (string, []string, error) {
	nonoPath, err := exec.LookPath("nono")
	if err != nil {
		return "", nil, fmt.Errorf("nono not found in PATH: %w", err)
	}
	args := []string{"nono", "wrap"}

	cwd, cwdErr := os.Getwd()
	if cwdErr == nil {
		if mainGit, ok := gitutil.DetectWorktreeGitDir(cwd); ok {
			args = append(args, "--allow", mainGit)
		}
	}

	if mcpConfigPath != "" {
		args = append(args, "--read-file", mcpConfigPath)
	}
	if brokerSocket != "" {
		args = append(args, "--allow-unix-socket", brokerSocket)
	}
	if profilePath != "" {
		args = append(args, "--profile", profilePath)
	}

	args = append(args, "claude")
	args = append(args, "--append-system-prompt", agentconfig.Pointer())

	settingsStr, err := settingsJSON(mcpConfigPath, cfg.ToolMode == "hook", denyRules)
	if err != nil {
		return "", nil, err
	}
	if settingsStr != "" {
		args = append(args, "--settings", settingsStr)
	}
	if cfg.ToolMode != "hook" {
		args = append(args, "--disallowed-tools", "Bash,Monitor")
	}
	if mcpConfigPath != "" {
		args = append(args, "--strict-mcp-config", "--mcp-config", mcpConfigPath)
	}

	args = append(args, opts.ClaudeOpts...)
	return nonoPath, args, nil
}

// runDeps holds the launcher's collaborators so run can be tested without
// touching the command broker, the real process, or os.Exit.
type runDeps struct {
	writeMCPConfig func(*config.Config) (string, func(), error)
	writeProfile   func(*config.Config) (path string, deny []string, cleanup func(), err error)
	startBroker    func(*config.Config) (socket string, cleanup func(), err error)
	supervise      func(path string, args []string) int
	exit           func(code int)
}

// Run generates the sandbox profile, starts the command broker, launches
// Claude under nono, and tears the broker down when Claude exits. It replaces
// the old syscall.Exec approach so the launcher can outlive Claude and run
// teardown.
func Run(cfg *config.Config, opts Options) error {
	return run(cfg, opts, runDeps{
		writeMCPConfig: writeGithubMCPConfig,
		writeProfile: func(c *config.Config) (string, []string, func(), error) {
			r, err := sandboxhost.Resolve(c, agentName)
			if err != nil {
				return "", nil, nil, err
			}
			path, cleanup, err := r.WriteProfile()
			if err != nil {
				return "", nil, nil, err
			}
			return path, r.DenyRules, cleanup, nil
		},
		startBroker: startCommandBroker,
		supervise:   superviseProcess,
		exit:        os.Exit,
	})
}

func run(cfg *config.Config, opts Options, d runDeps) error {
	var mcpConfigPath string
	var cleanupMCP func()
	if GithubMCPEnabled() {
		path, cleanup, err := d.writeMCPConfig(cfg)
		if err != nil {
			return fmt.Errorf("github mcp config: %w", err)
		}
		cleanupMCP = cleanup
		mcpConfigPath = path
	}
	defer func() {
		if cleanupMCP != nil {
			cleanupMCP()
		}
	}()

	profilePath, denyRules, cleanupProfile, err := d.writeProfile(cfg)
	if err != nil {
		return fmt.Errorf("sandbox host profile: %w", err)
	}
	defer func() {
		if cleanupProfile != nil {
			cleanupProfile()
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

	nonoPath, nonoArgs, err := BuildArgs(cfg, opts, mcpConfigPath, profilePath, denyRules, brokerSocket)
	if err != nil {
		return err
	}

	// superviseProcess inherits the launcher's environment, so setting it here
	// is the simplest correct way to hand the broker socket path to the child.
	os.Setenv(broker.SocketEnvVar, brokerSocket)

	code := d.supervise(nonoPath, nonoArgs)

	if cleanupMCP != nil {
		cleanupMCP()
		cleanupMCP = nil
	}
	if cleanupProfile != nil {
		cleanupProfile()
		cleanupProfile = nil
	}
	if cleanupBroker != nil {
		cleanupBroker()
		cleanupBroker = nil
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
// refuses to nest, and the broker must be the session entrypoint so its command
// policies apply to everything it executes. It is deliberately not given
// --allow-cwd; the working directory reaches the profile through --workdir,
// which is what $WORKDIR expands to inside it.
func BrokerArgs(cfg *config.Config, nonoPath, selfPath, sockPath, workdir string) []string {
	return []string{
		nonoPath, "run", "--silent",
		"--profile", cfg.CommandProfilePath(),
		"--workdir", workdir,
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
