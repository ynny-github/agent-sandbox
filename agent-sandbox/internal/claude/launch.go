// Package claude builds and runs the sandboxed `claude` command: it parses the
// launcher's arguments, constructs the `nono wrap … claude …` invocation
// (including the hook settings injected in hook mode), and executes it.
package claude

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
// hook mode, grants read-only access to the frozen policy snapshot at
// snapshotPath and injects the PreToolUse hook via `claude --settings`,
// routing it through that snapshot; otherwise it disables the Bash and
// Monitor tools. denyRules are folded into the injected settings as
// additional capability denies.
func BuildArgs(cfg *config.Config, opts Options, snapshotPath, mcpConfigPath,
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

	if cfg.ToolMode == "hook" && snapshotPath != "" {
		args = append(args, "--read-file", snapshotPath)
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

	settingsStr, err := settingsJSON(snapshotPath, mcpConfigPath, cfg.ToolMode == "hook", denyRules)
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
	writeSnapshot  func(*config.Config) (string, func(), error)
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
		writeSnapshot:  policysnapshot.Write,
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
	var snapshotPath string
	var cleanupSnapshot func()
	if cfg.ToolMode == "hook" {
		path, cleanup, err := d.writeSnapshot(cfg)
		if err != nil {
			return fmt.Errorf("policy snapshot: %w", err)
		}
		cleanupSnapshot = cleanup
		snapshotPath = path
	}
	// The deferred cleanup covers early error returns. On the success path we
	// call cleanupSnapshot explicitly before d.exit and nil it out, because
	// d.exit is os.Exit in production and os.Exit skips deferred functions.
	defer func() {
		if cleanupSnapshot != nil {
			cleanupSnapshot()
		}
	}()

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

	nonoPath, nonoArgs, err := BuildArgs(cfg, opts, snapshotPath, mcpConfigPath, profilePath, denyRules, brokerSocket)
	if err != nil {
		return err
	}

	// superviseProcess inherits the launcher's environment, so setting it here
	// is the simplest correct way to hand the broker socket path to the child.
	os.Setenv(broker.SocketEnvVar, brokerSocket)

	code := d.supervise(nonoPath, nonoArgs)

	if cleanupSnapshot != nil {
		cleanupSnapshot()
		cleanupSnapshot = nil
	}
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

// startCommandBroker generates the per-command sandbox profile, opens the
// broker socket, and starts serving. The returned cleanup closes the socket and
// removes the profile.
func startCommandBroker(cfg *config.Config) (string, func(), error) {
	nonoPath, err := exec.LookPath("nono")
	if err != nil {
		return "", nil, fmt.Errorf("nono not found in PATH: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, fmt.Errorf("getwd: %w", err)
	}

	resolved, err := sandboxhost.ResolveCommand(cfg, cwd)
	if err != nil {
		return "", nil, err
	}
	profilePath, cleanupProfile, err := resolved.WriteProfile()
	if err != nil {
		return "", nil, err
	}

	sockPath, err := brokerSocketPath()
	if err != nil {
		cleanupProfile()
		return "", nil, err
	}

	srv, err := broker.NewServer(sockPath, broker.NewNonoExecutor(nonoPath, profilePath))
	if err != nil {
		cleanupProfile()
		return "", nil, err
	}
	go srv.Serve()

	cleanup := func() {
		srv.Close()
		cleanupProfile()
	}
	return srv.SocketPath(), cleanup, nil
}

// brokerSocketPath returns a per-process socket path under
// policysnapshot.StateDir(). It stays short on purpose: unix socket paths are
// limited to about 104 bytes on macOS.
func brokerSocketPath() (string, error) {
	dir, err := policysnapshot.StateDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create state dir: %w", err)
	}
	return filepath.Join(dir, fmt.Sprintf("broker-%d.sock", os.Getpid())), nil
}
