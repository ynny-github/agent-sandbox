// agent-sandbox/cmd/claude.go
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/claude"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/envflag"
)

var claudeCmd = &cobra.Command{
	Use:                "claude [--config <path>] -- [claude-args...]",
	Short:              "Run Claude via nono sandbox",
	Args:               cobra.ArbitraryArgs,
	DisableFlagParsing: true,
	RunE:               runClaude,
}

func init() {
	rootCmd.AddCommand(claudeCmd)
}

func runClaude(cmd *cobra.Command, args []string) error {
	configFile, opts, err := claude.ParseArgs(args, configPath)
	if err != nil {
		return err
	}

	envKeys, err := envflag.Load(opts.EnvRefs)
	if err != nil {
		return err
	}

	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	if err := claude.ValidatePassthrough(opts.ClaudeOpts, claude.GithubMCPEnabled()); err != nil {
		return err
	}

	// Expose the --env keys to the sandboxed agent: nono forwards only vars
	// listed in the profile's allow_vars, and the values are already in this
	// process's env from envflag.Load above. They land on the agent section, not
	// the shared base, so --env grants the launched agent alone — widening a
	// brokered command's environment stays an explicit config edit.
	cfg.Sandbox.Agent.AllowEnv = append(cfg.Sandbox.Agent.AllowEnv, envKeys...)

	return claude.Run(cfg, opts)
}
