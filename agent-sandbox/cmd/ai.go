package cmd

import (
	"fmt"
	"io"
	"os"

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

// runConfigCheck loads the config and resolves both nono profiles from it,
// which is exactly what `agent-sandbox claude` does at launch. Loading alone
// would not be enough: capability names are only resolved in sandboxhost, so a
// typo there passes config.Load and surfaces at the next launch instead of
// here. A passing check therefore means the config is not what breaks it.
func runConfigCheck(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	if _, err := sandboxhost.Resolve(cfg, "claude"); err != nil {
		return fmt.Errorf("agent profile: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	if _, err := sandboxhost.ResolveShell(cfg, cwd); err != nil {
		return fmt.Errorf("shell profile: %w", err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "ok: %s resolves; it takes effect at the next `agent-sandbox claude` launch\n", configPath)

	grants, err := sandboxhost.ShellFilesystemGrants(cfg)
	if err != nil {
		return fmt.Errorf("shell filesystem grants: %w", err)
	}
	fmt.Fprintln(out, "\nA sandboxed command reaches, beyond the working directory:")
	printList(out, "read+write", grants.Write)
	printList(out, "read-only", grants.Read)

	domains, err := sandboxhost.ShellAllowDomains(cfg)
	if err != nil {
		return fmt.Errorf("shell allow domains: %w", err)
	}
	fmt.Fprintln(out, "\nExtra domains beyond the developer network profile:")
	printList(out, "", domains)
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
	fmt.Fprint(cmd.OutOrStdout(), agentconfig.Explain(cfg, configPath, safeWrappers()...))
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
