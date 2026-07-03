// agent-sandbox/cmd/claude.go
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/claude"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

var claudeCmd = &cobra.Command{
	Use:                "claude [nono-opts...] -- [claude-args...]",
	Short:              "Run Claude via nono sandbox",
	Args:               cobra.ArbitraryArgs,
	DisableFlagParsing: true,
	RunE:               runClaude,
}

func init() {
	rootCmd.AddCommand(claudeCmd)
}

func runClaude(cmd *cobra.Command, args []string) error {
	configFile, opts := claude.ParseArgs(args, configPath)

	if err := claude.ValidatePassthrough(opts.ClaudeOpts); err != nil {
		return err
	}

	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	return claude.Run(cfg, opts)
}
