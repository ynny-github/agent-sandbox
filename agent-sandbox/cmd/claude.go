// agent-sandbox/cmd/claude.go
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/internal/claude"
	"github.com/ynny-github/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/internal/envflag"
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

	// --env loads the referenced file's variables into this process, which is
	// all it does now: nono forwards only what the agent profile's
	// environment.allow_vars lists, and that list is hand-written.
	if _, err := envflag.Load(opts.EnvRefs); err != nil {
		return err
	}

	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	if err := claude.ValidatePassthrough(opts.ClaudeOpts); err != nil {
		return err
	}

	return claude.Run(cfg, opts)
}
