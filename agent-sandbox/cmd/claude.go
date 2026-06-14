// agent-sandbox/cmd/claude.go
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/gitutil"
)

var claudeCmd = &cobra.Command{
	Use:   "claude [-- <claude-args>...]",
	Short: "Run Claude via nono sandbox",
	Args:  cobra.ArbitraryArgs,
	RunE:  runClaude,
}

func init() {
	rootCmd.AddCommand(claudeCmd)
}

func validateClaudePassthrough(args []string) error {
	for _, arg := range args {
		if strings.HasPrefix(arg, "--settings") {
			return fmt.Errorf("--settings is not allowed")
		}
	}
	return nil
}

func buildNonoArgs(cfg *config.Config) (string, []string, error) {
	if cfg.Nono.Profile == "" {
		return "", nil, fmt.Errorf("[nono] profile not set in agent-sandbox.toml")
	}
	nonoPath, err := exec.LookPath("nono")
	if err != nil {
		return "", nil, fmt.Errorf("nono not found in PATH: %w", err)
	}
	sub := cfg.Nono.Subcommand
	if sub == "" {
		sub = "run"
	}
	args := []string{"nono", sub, "--profile", cfg.Nono.Profile}
	if cwd, err := os.Getwd(); err == nil {
		if mainGit, ok := gitutil.DetectWorktreeGitDir(cwd); ok {
			args = append(args, "--allow", mainGit)
		}
	}
	args = append(args, "claude", "--disallowed-tools", "Bash,Monitor")
	return nonoPath, args, nil
}

func runClaude(cmd *cobra.Command, args []string) error {
	var claudeArgs []string
	if dashIdx := cmd.ArgsLenAtDash(); dashIdx >= 0 {
		claudeArgs = args[dashIdx:]
	}

	if err := validateClaudePassthrough(claudeArgs); err != nil {
		return err
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	nonoPath, nonoArgs, err := buildNonoArgs(cfg)
	if err != nil {
		return err
	}
	nonoArgs = append(nonoArgs, claudeArgs...)

	if err := syscall.Exec(nonoPath, nonoArgs, os.Environ()); err != nil {
		return fmt.Errorf("exec nono: %w", err)
	}
	return nil
}
