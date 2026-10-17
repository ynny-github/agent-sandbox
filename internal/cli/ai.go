package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/internal/agentconfig"
	"github.com/ynny-github/agent-sandbox/internal/launch"
)

var aiCmd = &cobra.Command{
	Use:   "ai",
	Short: "AI-agent-facing helpers",
}

var explainCmd = &cobra.Command{
	Use:   "explain",
	Short: "Explain how to use the sandbox environment",
	Args:  cobra.NoArgs,
	RunE:  runExplain,
}

var pointerCmd = &cobra.Command{
	Use:   "pointer",
	Short: "Print the system-prompt line that points the agent at `agent-sandbox ai explain`",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprint(cmd.OutOrStdout(), agentconfig.Pointer())
	},
}

func init() {
	aiCmd.AddCommand(pointerCmd)
	explainCmd.Flags().StringVar(&explainCommandProfile, "command-profile",
		filepath.Join(launch.Dir, "command-profile.json"), "execd's nono profile")
	explainCmd.Flags().StringVar(&explainAgentProfile, "agent-profile",
		filepath.Join(launch.Dir, "claude-profile.json"), "the agent's nono profile")
	aiCmd.AddCommand(explainCmd)
	rootCmd.AddCommand(aiCmd)
}

// runConfigCheck loads the config and asks nono to validate both profiles —
// the same two files a launch hands to nono. agent-sandbox does not interpret
// either one, so a passing check means the config and the profiles are not
// what breaks the next launch; what they *grant* is `nono profile show`.
// explainCommandProfile and explainAgentProfile name the profiles explain
// points the agent at; the defaults are where init and the launch template
// put them.
var explainCommandProfile, explainAgentProfile string

func runExplain(cmd *cobra.Command, args []string) error {
	fmt.Fprint(cmd.OutOrStdout(), agentconfig.Explain(explainCommandProfile, explainAgentProfile))
	return nil
}
