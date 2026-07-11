package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/agentconfig"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

var aiCmd = &cobra.Command{
	Use:   "ai",
	Short: "AI-agent-facing helpers",
}

var explainCmd = &cobra.Command{
	Use:   "explain",
	Short: "Explain how to use the sandbox environment, from the active config",
	Args:  cobra.NoArgs,
	RunE:  runExplain,
}

func init() {
	aiCmd.AddCommand(explainCmd)
	rootCmd.AddCommand(aiCmd)
}

func runExplain(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	fmt.Fprint(cmd.OutOrStdout(), agentconfig.Explain(cfg, safeWrappers()...))
	return nil
}

// safeWrappers lists the registered `safe` subcommands (excluding cobra's
// generated help) so `ai explain` can point agents at them.
func safeWrappers() []agentconfig.SafeCommand {
	var wrappers []agentconfig.SafeCommand
	for _, c := range safeCmd.Commands() {
		if c.Hidden || c.Name() == "help" {
			continue
		}
		wrappers = append(wrappers, agentconfig.SafeCommand{Use: c.Use, Short: c.Short})
	}
	return wrappers
}
