// agent-sandbox/cmd/generate.go
package cmd

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/agentconfig"
)

var generateCmd = &cobra.Command{
	Use:   "generate [claude|agents|gemini]",
	Short: "Print agent configuration to stdout",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runGenerate,
}

func runGenerate(cmd *cobra.Command, args []string) error {
	format := "claude"
	if len(args) > 0 {
		format = args[0]
	}
	return agentconfig.Print(format, os.Stdout)
}

func init() {
	rootCmd.AddCommand(generateCmd)
}
