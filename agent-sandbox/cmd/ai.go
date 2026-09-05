package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/agentconfig"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxhost"
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
	Short: "Validate the config the way `agent-sandbox claude` will read it at launch",
	Args:  cobra.NoArgs,
	RunE:  runConfigCheck,
}

func init() {
	aiCmd.AddCommand(explainCmd)
	aiCmd.AddCommand(configCheckCmd)
	rootCmd.AddCommand(aiCmd)
}

// runConfigCheck loads the config and resolves the agent's nono profile from
// it, which is exactly what `agent-sandbox claude` does at launch. Loading
// alone would not be enough: capability names are only resolved in
// sandboxhost, so a typo there passes config.Load and surfaces at the next
// launch instead of here. A passing check therefore means the config is not
// what breaks it.
//
// The command profile brokered commands run under is not generated or read by
// agent-sandbox at all — nono is what validates it — so there is nothing of
// it to resolve here.
func runConfigCheck(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	resolved, err := sandboxhost.Resolve(cfg, "claude")
	if err != nil {
		return fmt.Errorf("agent profile: %w", err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "ok: %s resolves; it takes effect at the next `agent-sandbox claude` launch\n", configPath)

	grants := resolved.FilesystemGrants()
	fmt.Fprintln(out, "\nThe launched agent's own sandbox additionally reaches:")
	printList(out, "read+write", grants.Write)
	printList(out, "read-only", grants.Read)
	return nil
}

// printList writes one indented line per entry, or "(none)" when empty, so the
// output distinguishes an empty grant list from a missing section.
func printList(out io.Writer, label string, items []string) {
	if label != "" {
		label += ": "
	}
	if len(items) == 0 {
		fmt.Fprintf(out, "  %s(none)\n", label)
		return
	}
	for _, item := range items {
		fmt.Fprintf(out, "  %s%s\n", label, item)
	}
}

func runExplain(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	fmt.Fprint(cmd.OutOrStdout(), agentconfig.Explain(cfg, configPath))
	return nil
}
