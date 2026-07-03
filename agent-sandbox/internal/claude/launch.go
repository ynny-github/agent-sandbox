// Package claude builds and runs the sandboxed `claude` command: it parses the
// launcher's arguments, constructs the `nono wrap … claude …` invocation
// (including the hook settings injected in hook mode), and executes it.
package claude

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/agentconfig"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/gitutil"
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
// Claude under the sandbox for cfg. In hook mode it injects the PreToolUse hook
// via `claude --settings`; otherwise it disables the Bash and Monitor tools.
func BuildArgs(cfg *config.Config, opts Options) (string, []string, error) {
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
	args = append(args, "claude")
	args = append(args, "--append-system-prompt", agentconfig.Pointer())

	if cfg.ToolMode == "hook" {
		settingsJSON, err := hookSettingsJSON()
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

// Run builds the launch command for cfg and replaces the current process with
// it via syscall.Exec. It returns only on failure.
func Run(cfg *config.Config, opts Options) error {
	nonoPath, nonoArgs, err := BuildArgs(cfg, opts)
	if err != nil {
		return err
	}
	if err := syscall.Exec(nonoPath, nonoArgs, os.Environ()); err != nil {
		return fmt.Errorf("exec nono: %w", err)
	}
	return nil
}
