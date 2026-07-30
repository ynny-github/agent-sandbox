// agent-sandbox/cmd/root.go
package cmd

import (
	"runtime/debug"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/envflag"
)

var configPath string
var envRefs []string

func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "dev"
}

var rootCmd = &cobra.Command{
	Use:               "agent-sandbox",
	Short:             "Agent sandbox command router",
	SilenceUsage:      true,
	PersistentPreRunE: applyPersistentEnv,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "agent-sandbox.toml", "path to TOML config file")
	rootCmd.PersistentFlags().StringArrayVar(&envRefs, "env", nil,
		"load env vars from a source into the process (repeatable), e.g. --env file:.env")
}

// applyPersistentEnv loads the --env sources into the process environment for
// normal (flag-parsed) commands such as `exec`. Commands that disable flag
// parsing (claude, debug) leave envRefs empty here and apply --env themselves
// from their own argument parsing.
func applyPersistentEnv(cmd *cobra.Command, args []string) error {
	_, err := envflag.Load(envRefs)
	return err
}
