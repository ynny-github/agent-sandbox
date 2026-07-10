// Package claude builds and runs the sandboxed `claude` command: it parses the
// launcher's arguments, constructs the `nono wrap … claude …` invocation
// (including the hook settings injected in hook mode), and executes it.
package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/agentconfig"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/gitutil"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/policysnapshot"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxlifecycle"
)

// Options is the split invocation for the claude launcher: options passed
// through to `nono wrap` and options passed through to `claude`.
type Options struct {
	NonoOpts   []string
	ClaudeOpts []string
}

// ParseArgs splits the raw args passed to the claude/debug command into the
// config-file path and the nono/claude passthrough options. The first
// standalone "--" separates nono options (before) from claude options (after);
// with no "--", every token is a nono option. A "--config <val>" or
// "--config=<val>" appearing in the nono region sets the config path and is
// removed from the nono options. defaultConfig is used when no "--config" is
// given. This is needed because the command disables cobra flag parsing to pass
// options verbatim.
func ParseArgs(args []string, defaultConfig string) (configFile string, opts Options) {
	configFile = defaultConfig

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
		default:
			opts.NonoOpts = append(opts.NonoOpts, a)
		}
	}
	return configFile, opts
}

// ValidatePassthrough rejects claude passthrough options that agent-sandbox
// reserves for itself (currently --settings, used to inject the hook config).
func ValidatePassthrough(claudeOpts []string) error {
	for _, arg := range claudeOpts {
		if strings.HasPrefix(arg, "--settings") {
			return fmt.Errorf("--settings is not allowed")
		}
	}
	return nil
}

// BuildArgs constructs the nono executable path and the argv used to launch
// Claude under the sandbox for cfg. In hook mode it grants read-only access to
// the frozen policy snapshot at snapshotPath and injects the PreToolUse hook
// via `claude --settings`, routing it through that snapshot; otherwise it
// disables the Bash and Monitor tools.
func BuildArgs(cfg *config.Config, opts Options, snapshotPath string) (string, []string, error) {
	nonoPath, err := exec.LookPath("nono")
	if err != nil {
		return "", nil, fmt.Errorf("nono not found in PATH: %w", err)
	}
	args := []string{"nono", "wrap"}
	args = append(args, opts.NonoOpts...)

	cwd, cwdErr := os.Getwd()
	if cwdErr == nil {
		if mainGit, ok := gitutil.DetectWorktreeGitDir(cwd); ok {
			args = append(args, "--allow", mainGit)
		}
	}

	if cfg.ToolMode == "hook" && snapshotPath != "" {
		args = append(args, "--read-file", snapshotPath)
	}

	args = append(args, "claude")
	args = append(args, "--append-system-prompt", agentconfig.Pointer())

	if cfg.ToolMode == "hook" {
		settingsJSON, err := hookSettingsJSON(snapshotPath)
		if err != nil {
			return "", nil, err
		}
		args = append(args, "--settings", settingsJSON)
	} else {
		args = append(args, "--disallowed-tools", "Bash,Monitor")
	}

	args = append(args, opts.ClaudeOpts...)
	return nonoPath, args, nil
}

// sandboxHandle is the lifecycle surface the launcher needs after ensuring the
// sandbox is up. *sandboxlifecycle.Result satisfies it.
type sandboxHandle interface {
	Started() bool
	Down(context.Context) error
	Close()
}

// runDeps holds the launcher's collaborators so run can be tested without
// touching Docker, the real process, or os.Exit.
type runDeps struct {
	writeSnapshot func(*config.Config) (string, func(), error)
	ensureUp      func(context.Context, *config.Config) (sandboxHandle, error)
	supervise     func(path string, args []string) int
	exit          func(code int)
}

// Run ensures the sandbox is up, launches Claude under it, and — if this call
// started the sandbox — tears it down when Claude exits. It replaces the old
// syscall.Exec approach so the launcher can outlive Claude and run teardown.
func Run(cfg *config.Config, opts Options) error {
	return run(cfg, opts, runDeps{
		writeSnapshot: policysnapshot.Write,
		ensureUp: func(ctx context.Context, c *config.Config) (sandboxHandle, error) {
			return sandboxlifecycle.EnsureUp(ctx, c)
		},
		supervise: superviseProcess,
		exit:      os.Exit,
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

	nonoPath, nonoArgs, err := BuildArgs(cfg, opts, snapshotPath)
	if err != nil {
		return err
	}

	handle, err := d.ensureUp(context.Background(), cfg)
	if err != nil {
		return fmt.Errorf("sandbox not available: %w (run `agent-sandbox doctor`)", err)
	}
	defer handle.Close()

	code := d.supervise(nonoPath, nonoArgs)

	if handle.Started() {
		downCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if derr := handle.Down(downCtx); derr != nil {
			fmt.Fprintf(os.Stderr, "warning: sandbox down failed: %v\n", derr)
		}
	}

	handle.Close()
	if cleanupSnapshot != nil {
		cleanupSnapshot()
		cleanupSnapshot = nil
	}
	d.exit(code)
	return nil
}
