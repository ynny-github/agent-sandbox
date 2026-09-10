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

var configCheckCmd = &cobra.Command{
	Use:   "config-check",
	Short: "Validate the config and both nono profiles the way `agent-sandbox claude` will read them at launch",
	Args:  cobra.NoArgs,
	RunE:  runConfigCheck,
}

func init() {
	aiCmd.AddCommand(explainCmd)
	aiCmd.AddCommand(configCheckCmd)
	rootCmd.AddCommand(aiCmd)
}

// runConfigCheck loads the config and asks nono to validate both profiles —
// the same two files a launch hands to nono. agent-sandbox does not interpret
// either one, so a passing check means the config and the profiles are not
// what breaks the next launch; what they *grant* is `nono profile show`.
func runConfigCheck(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "ok: %s loads\n", configPath)

	for _, p := range []struct{ label, path string }{
		{"agent profile", cfg.AgentProfilePath("claude")},
		{"command profile", cfg.CommandProfilePath()},
	} {
		if err := validateProfile(p.path); err != nil {
			return fmt.Errorf("%s: %w", p.label, err)
		}
		fmt.Fprintf(out, "ok: %s %s validates\n", p.label, p.path)
	}

	fmt.Fprint(out, profileHelp)
	return nil
}

func runExplain(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	fmt.Fprint(cmd.OutOrStdout(), agentconfig.Explain(cfg, configPath))
	return nil
}
