package cmd

import "github.com/spf13/cobra"

var safeCmd = &cobra.Command{
	Use:   "safe",
	Short: "Safe wrappers that validate a tool's invocation before running it",
}

func init() {
	rootCmd.AddCommand(safeCmd)
}
