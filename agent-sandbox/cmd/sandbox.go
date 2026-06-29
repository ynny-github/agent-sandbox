package cmd

import "github.com/spf13/cobra"

var sandboxCmd = &cobra.Command{
	Use:   "sandbox",
	Short: "Manage the sandbox container lifecycle",
}

func init() {
	rootCmd.AddCommand(sandboxCmd)
}
