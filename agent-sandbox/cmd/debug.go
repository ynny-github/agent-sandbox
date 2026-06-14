// agent-sandbox/cmd/debug.go
package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

var debugCmd = &cobra.Command{
	Use:   "debug",
	Short: "Show the command that would be used to run Claude",
	RunE:  runDebug,
}

func init() {
	rootCmd.AddCommand(debugCmd)
}

func runDebug(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	_, nonoArgs, err := buildNonoArgs(cfg)
	if err != nil {
		return err
	}
	fmt.Println(strings.Join(nonoArgs, " "))
	return nil
}
