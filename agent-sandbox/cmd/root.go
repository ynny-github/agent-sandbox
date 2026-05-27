// agent-sandbox/cmd/root.go
package cmd

import (
	"runtime/debug"

	"github.com/spf13/cobra"
)

var configPath string

func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "dev"
}

var rootCmd = &cobra.Command{
	Use:          "agent-sandbox",
	Short:        "Agent sandbox command router",
	SilenceUsage: true,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "agent-sandbox.toml", "path to TOML config file")
}
