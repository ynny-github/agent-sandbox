// Package claude builds and runs the sandboxed `claude` command: it parses the
// launcher's arguments, constructs the `nono run … claude …` invocation
// (including the injected PreToolUse hook settings), and executes it.
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ynny-github/agent-sandbox/internal/agentconfig"
	"github.com/ynny-github/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/internal/execd"
	"github.com/ynny-github/agent-sandbox/internal/gitutil"
)

// agentName identifies the launched agent for host-policy resolution. Only
// "claude" exists today; new agents pass their own identifier.
const agentName = "claude"

// Options carries the claude passthrough options (everything after "--").
// agent-sandbox no longer forwards options to nono.
type Options struct {
	ClaudeOpts []string
	EnvRefs    []string
	// ContextMode reports whether --context-mode was passed. It selects
	// context-mode's execd backend for this session and nothing else; see
	// contextmode.go.
	ContextMode bool
}

// ParseArgs splits the raw args into the config-file path and the claude
// passthrough options. The first standalone "--" separates agent-sandbox's own
// region (before) from claude options (after). Only "--config <val>" /
// "--config=<val>", "--env <ref>" / "--env=<ref>", and "--context-mode"
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
		case a == "--context-mode":
			opts.ContextMode = true
		case a == "--profile" || a == "-p" || strings.HasPrefix(a, "--profile="):
			return "", Options{}, fmt.Errorf("--profile is no longer accepted; configure the sandbox profile via [agents.<name>].profile in agent-sandbox.toml")
		default:
			return "", Options{}, fmt.Errorf("unexpected option %q before \"--\": agent-sandbox no longer forwards options to nono; only --config, --env and --context-mode are accepted before \"--\", and claude options go after \"--\"", a)
		}
	}
	return configFile, opts, nil
}

// ValidatePassthrough rejects the one claude passthrough option agent-sandbox
// reserves for itself: --settings, which carries the PreToolUse hook that
// routes every command through execd. --mcp-config and
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

// ResolveExecd finds the execd binary on PATH and resolves symlinks, because
// Landlock checks the target and a grant has to name it. startExecd calls it
// once per launch and run feeds the same path to both sessions, so they cannot
// disagree about which execd they run.
func ResolveExecd() (string, error) {
	p, err := exec.LookPath("execd")
	if err != nil {
		return "", fmt.Errorf("execd not found in PATH; install it with "+
			"`go install github.com/ynny-github/agent-sandbox/cmd/execd@latest`: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
		return resolved, nil
	}
	return p, nil
}

// hookProbePayload is a minimal PreToolUse payload; hookProbeWant is what the
// hook must rewrite it into. The command is `true` so that nothing runs even if
// the response were somehow acted on.
const (
	hookProbePayload = `{"tool_name":"Bash","tool_input":{"command":"true"}}`
	hookProbeWant    = "execd exec --"
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
// shape doctor uses for the execd socket variable.
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
			"through execd: %s", profilePath, strings.TrimSpace(string(out)))
	}
	return nil
}

// HerdrAgentEnvVar is herdr's foreground-process hint. Under `nono run`,
// claude runs on a pty nono allocates, so herdr sees only the launcher and
// nono in the pane's foreground job and cannot identify the agent by name.
// herdr reads this variable from /proc/<pid>/environ of a process in that job
// instead — and that file shows the environment as of execve. nono soon makes
// its own environ unreadable, and an os.Setenv in the launcher never shows up
// there, so the hint has to be in the launcher's own exec-time environment:
// see herdrReexecEnv. Measured with herdr 0.9.1: set by os.Setenv, herdr found
// the agent only when it polled before nono locked its environ (4 of 5
// launches); present at the launcher's exec, it found it every time. The
// agent profile does not forward it, so it never reaches claude itself.
const HerdrAgentEnvVar = "HERDR_AGENT"

// herdrPaneEnvVar is set by herdr in every pane it runs. The launcher re-execs
// with HerdrAgentEnvVar only when it is present: outside herdr nothing reads
// the hint.
const herdrPaneEnvVar = "HERDR_PANE_ID"

// BuildArgs constructs the nono executable path and the argv used to launch
// Claude under the sandbox for cfg. It injects the operator's profile at
// profilePath via `--profile` (no user nono options are forwarded) and the
// PreToolUse hook via `claude --settings`. The injected settings carry the
// hook only; the profile contributes nothing to them.
func BuildArgs(cfg *config.Config, opts Options,
	profilePath, execdSocket, shellWrapper, execdPath string) (string, []string, error) {
	nonoPath, err := exec.LookPath("nono")
	if err != nil {
		return "", nil, fmt.Errorf("nono not found in PATH: %w", err)
	}
	// Supervised `nono run`, not `nono wrap`: on nono 0.78.0 wrap sandboxes
	// nono itself before it runs the profile's session_hooks.before, so the
	// nolabs-ai/claude pack's before-hook fails with "Failed to create
	// session env directory ...: Permission denied". run keeps nono resident
	// as claude's parent; it forwards SIGTERM/SIGHUP to claude and exits with
	// claude's status, so superviseProcess still sees claude's exit code.
	//
	// --allow-cwd shares the working directory without nono's interactive
	// "Share <cwd> with read+write access? [y/N]" prompt, which the
	// nolabs-ai/claude base profile otherwise triggers on every launch. The
	// access level still comes from the profile.
	args := []string{"nono", "run", "--allow-cwd"}

	// The agent runs `agent-sandbox hook` as its own direct child — inside its
	// own sandbox, not through execd — so the launcher's binary has to be
	// reachable from the agent profile. Without it nono refuses the execve and
	// every command fails with nothing on screen to explain it.
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

	// The hook rewrites every command to `execd exec -- …`, which the agent
	// runs as its own child, so execd's binary needs the same read grant as
	// the launcher's own, for the same reason.
	if execdPath != "" {
		args = append(args, "--read-file", execdPath)
	}

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

	if execdSocket != "" {
		args = append(args, "--allow-unix-socket", execdSocket)
	}
	if profilePath != "" {
		args = append(args, "--profile", profilePath)
	}

	args = append(args, "claude")
	args = append(args, "--append-system-prompt", agentconfig.Pointer())

	settingsStr, err := settingsJSON()
	if err != nil {
		return "", nil, err
	}
	args = append(args, "--settings", settingsStr)

	args = append(args, opts.ClaudeOpts...)
	return nonoPath, args, nil
}

// runDeps holds the launcher's collaborators so run can be tested without
// touching execd, the real process, or os.Exit.
type runDeps struct {
	// agentProfile resolves — and existence-checks — the nono profile the
	// launched agent runs under. It stays a dependency so tests can drive run
	// without touching the filesystem.
	agentProfile func(*config.Config) (string, error)
	// verifyHook proves the PreToolUse hook can actually run under the agent's
	// profile. Every session injects that hook, so every launch runs it.
	verifyHook func(profilePath, selfPath string) error
	// contextModeEnabled reports whether Claude Code has an enabled
	// context-mode plugin. Called only when --context-mode was passed, and
	// before anything is started, so a refusal tears nothing down.
	contextModeEnabled func() (bool, error)
	// verifyContextMode proves the backend selection actually reaches the
	// sandbox. Called only when --context-mode was passed, after execd is up
	// and the variables are set.
	verifyContextMode func(profilePath string) error
	startExecd        func(*config.Config) (socket, execdPath string, cleanup func(), err error)
	// startShellWrapper writes the shell Claude runs tool commands with. It
	// returns the wrapper's path and a cleanup that removes it.
	startShellWrapper func() (path string, cleanup func(), err error)
	// reexec replaces the launcher with a fresh copy of itself (syscall.Exec);
	// it returns only on failure.
	reexec    func(path string, argv, env []string) error
	supervise func(path string, args []string) int
	exit      func(code int)
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

// Run generates the sandbox profile, starts execd, launches
// Claude under nono, and tears execd down when Claude exits. It replaces
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
		agentProfile:       defaultAgentProfile,
		verifyHook:         probeHook,
		contextModeEnabled: contextModeEnabled,
		verifyContextMode:  probeContextMode,
		startExecd:         startExecd,
		startShellWrapper:  defaultShellWrapper,
		reexec:             syscall.Exec,
		supervise:          superviseProcess,
		exit:               os.Exit,
	}
}

// herdrReexecEnv returns environ plus HerdrAgentEnvVar=claude, and true, when
// the launcher runs inside a herdr pane without the hint. An operator-set hint
// wins, and it is also what stops the re-exec'd launcher from re-exec'ing.
func herdrReexecEnv(environ []string) ([]string, bool) {
	if os.Getenv(herdrPaneEnvVar) == "" || os.Getenv(HerdrAgentEnvVar) != "" {
		return nil, false
	}
	env := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		if !strings.HasPrefix(kv, HerdrAgentEnvVar+"=") {
			env = append(env, kv)
		}
	}
	return append(env, HerdrAgentEnvVar+"=claude"), true
}

func run(cfg *config.Config, opts Options, d runDeps) error {
	// First, before anything is started: the re-exec'd launcher runs all of
	// this again, and anything started here would be orphaned.
	if env, ok := herdrReexecEnv(os.Environ()); ok {
		self, err := launcherPath()
		if err != nil {
			return err
		}
		if err := d.reexec(self, os.Args, env); err != nil {
			return fmt.Errorf("re-exec with %s: %w", HerdrAgentEnvVar, err)
		}
		return nil
	}

	profilePath, err := d.agentProfile(cfg)
	if err != nil {
		return err
	}

	self, selfErr := launcherPath()
	if selfErr != nil {
		return selfErr
	}
	if err := d.verifyHook(profilePath, self); err != nil {
		return fmt.Errorf("hook check: %w", err)
	}

	if opts.ContextMode {
		enabled, cerr := d.contextModeEnabled()
		if cerr != nil {
			return fmt.Errorf("context-mode check: %w", cerr)
		}
		if !enabled {
			return fmt.Errorf("context-mode check: --context-mode was passed, but " +
				"`claude plugin list --json` reports no enabled context-mode plugin; " +
				"install and enable it, or drop the flag")
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

	execdSocket, execdPath, cleanupExecd, err := d.startExecd(cfg)
	if err != nil {
		return fmt.Errorf("exec daemon: %w", err)
	}
	defer func() {
		if cleanupExecd != nil {
			cleanupExecd()
		}
	}()

	nonoPath, nonoArgs, err := BuildArgs(cfg, opts, profilePath, execdSocket, shellWrapper, execdPath)
	if err != nil {
		return err
	}

	// superviseProcess inherits the launcher's environment, so setting it here
	// is the simplest correct way to hand the execd socket path to the child.
	os.Setenv(execd.SocketEnvVar, execdSocket)
	if shellWrapper != "" {
		os.Setenv(ShellEnvVar, shellWrapper)
	}
	// Published only when the flag asked for it, and unset otherwise: with
	// CONTEXT_MODE_EXEC_BACKEND in the agent profile's allow_vars, a value the
	// operator exported in their own shell would otherwise be forwarded into a
	// session that never made this selection and whose probe never verified
	// it. context-mode treats an absent variable as "local".
	if opts.ContextMode {
		os.Setenv(ContextModeEnvVar, ContextModeExecd)
		if err := d.verifyContextMode(profilePath); err != nil {
			return fmt.Errorf("context-mode check: %w", err)
		}
	} else {
		os.Unsetenv(ContextModeEnvVar)
	}

	code := d.supervise(nonoPath, nonoArgs)

	if cleanupExecd != nil {
		cleanupExecd()
		cleanupExecd = nil
	}
	if cleanupWrapper != nil {
		cleanupWrapper()
		cleanupWrapper = nil
	}
	d.exit(code)
	return nil
}

// startExecd resolves the execd binary on PATH, refuses one the command
// profile could replace, and runs `execd up` for this working directory and
// the command profile. It returns the socket execd up printed, the resolved
// execd path, and a cleanup that runs `execd down` on that socket.
//
// execd starts its session inside a sandbox whose command profile governs
// everything it executes, which is the whole point: a process outside the
// sandbox would execute commands with the launcher's own reach.
func startExecd(cfg *config.Config) (string, string, func(), error) {
	// The writability check below runs `nono why`; fail on a missing nono
	// first, with the error that names it.
	if _, err := exec.LookPath("nono"); err != nil {
		return "", "", nil, fmt.Errorf("nono not found in PATH: %w", err)
	}
	execdPath, err := ResolveExecd()
	if err != nil {
		return "", "", nil, err
	}
	// A command that can write execd, or the directory it sits in, could
	// choose what this launch grants the agent and runs as the daemon —
	// including by planting an `execd` in a writable directory earlier on
	// PATH. Planting happens between launches, so refusing it only in doctor
	// is not enough.
	for _, p := range []string{execdPath, filepath.Dir(execdPath)} {
		writable, werr := ProfileAllowsWrite(context.Background(), combinedOutput, cfg.CommandProfilePath(), p)
		if werr != nil {
			return "", "", nil, fmt.Errorf("check that the command profile cannot write %s: %w", p, werr)
		}
		if writable {
			return "", "", nil, fmt.Errorf("the command profile can write %s, so a command could replace the "+
				"execd this launch runs; install execd outside every path the command profile grants "+
				"write access to", p)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", nil, fmt.Errorf("getwd: %w", err)
	}
	// execd owns the session: where the socket lives, the nono argv, waiting
	// for it, and stopping it. The launcher only decided which execd to trust.
	up := exec.Command(execdPath, "up", "--profile", cfg.CommandProfilePath(), "--workdir", cwd)
	up.Stderr = os.Stderr
	out, err := up.Output()
	if err != nil {
		return "", "", nil, fmt.Errorf("execd up: %w", err)
	}
	sock := strings.TrimSpace(string(out))
	cleanup := func() {
		down := exec.Command(execdPath, "down", sock)
		down.Stdout, down.Stderr = os.Stderr, os.Stderr
		if err := down.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "agent-sandbox: execd down: %v\n", err)
		}
	}
	return sock, execdPath, cleanup, nil
}

// ProfileAllowsWrite asks nono whether profilePath grants write access to
// binPath, running nono through run (which returns combined output). The answer comes from `nono why --json`, whose status field is
// "allowed" or "denied"; the command exits 0 either way, so the status is the
// only signal.
//
// nono writes warnings (a bypass_protection entry naming a path that does not
// exist on this host, for one) to stderr, and run is expected to combine the streams,
// so the JSON object is found rather than assumed to start at byte zero.
func ProfileAllowsWrite(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error),
	profilePath, binPath string) (bool, error) {
	out, err := run(ctx, "nono", "why", "--json",
		"--profile", profilePath, "--path", binPath, "--op", "write")
	if err != nil {
		return false, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	i := strings.IndexByte(string(out), '{')
	if i < 0 {
		return false, fmt.Errorf("no JSON in nono why output: %s", strings.TrimSpace(string(out)))
	}
	var answer struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(string(out)[i:]), &answer); err != nil {
		return false, err
	}
	switch answer.Status {
	case "allowed":
		return true, nil
	case "denied":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected nono why status %q", answer.Status)
	}
}

// combinedOutput is the real runner ProfileAllowsWrite uses at launch.
func combinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
