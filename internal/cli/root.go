// internal/cli/root.go
package cli

import (
	"runtime/debug"

	"github.com/spf13/cobra"
)

func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "dev"
}

var rootCmd = &cobra.Command{
	Use:          "agent-sandbox",
	Short:        "Sandbox helpers for running an AI coding agent under nono with execd",
	SilenceUsage: true,
}

func Execute() error {
	return rootCmd.Execute()
}

// applyPersistentEnv loads the --env sources into the process environment for
// normal (flag-parsed) commands such as `exec`. Commands that disable flag
// parsing (claude, debug) leave envRefs empty here and apply --env themselves
// from their own argument parsing.
