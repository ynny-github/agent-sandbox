// agent-sandbox/cmd/debug.go
package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/claude"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/policysnapshot"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxhost"
)

var debugCmd = &cobra.Command{
	Use:                "debug [nono-opts...] -- [claude-args...]",
	Short:              "Show the command that would be used to run Claude",
	DisableFlagParsing: true,
	RunE:               runDebug,
}

func init() {
	rootCmd.AddCommand(debugCmd)
}

func runDebug(cmd *cobra.Command, args []string) error {
	configFile, opts := claude.ParseArgs(args, configPath)
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	var snapshotPath string
	if cfg.ToolMode == "hook" {
		path, cleanup, werr := policysnapshot.Write(cfg)
		if werr != nil {
			return fmt.Errorf("policy snapshot: %w", werr)
		}
		defer cleanup()
		snapshotPath = path
	}

	r, err := sandboxhost.Resolve(cfg, "claude")
	if err != nil {
		return err
	}
	profilePath, cleanupProfile, err := r.WriteProfile()
	if err != nil {
		return err
	}
	defer cleanupProfile()

	_, nonoArgs, err := claude.BuildArgs(cfg, opts, snapshotPath, "", profilePath, r.DenyRules)
	if err != nil {
		return err
	}
	fmt.Println(strings.Join(nonoArgs, " "))
	return nil
}
