// agent-sandbox/cmd/debug.go
package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/claude"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/policysnapshot"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxhost"
)

var debugCmd = &cobra.Command{
	Use:                "debug [--config <path>] -- [claude-args...]",
	Short:              "Show the command that would be used to run Claude",
	DisableFlagParsing: true,
	RunE:               runDebug,
}

func init() {
	rootCmd.AddCommand(debugCmd)
}

func runDebug(cmd *cobra.Command, args []string) error {
	configFile, opts, err := claude.ParseArgs(args, configPath)
	if err != nil {
		return err
	}
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

	profileJSON, err := r.ProfileJSON()
	if err != nil {
		return err
	}
	mcpJSON, err := claude.RedactedGithubMCPConfigJSON()
	if err != nil {
		return err
	}
	fmt.Print(formatGeneratedConfigs(profilePath, profileJSON, cfg.Claude.GithubMCP.Enabled, mcpJSON))
	return nil
}

// formatGeneratedConfigs renders the generated nono profile (no secrets) and
// the token-redacted GitHub MCP config for display under the debug command, so
// the exact files the launcher writes can be inspected without hunting for temp
// files. The MCP token is always redacted — it never reaches the terminal.
func formatGeneratedConfigs(profilePath string, profileJSON []byte, mcpEnabled bool, mcpJSON []byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n# generated nono profile (%s):\n", profilePath)
	b.WriteString(indentJSON(profileJSON))
	b.WriteString("\n")

	state := "disabled"
	if mcpEnabled {
		state = "enabled"
	}
	fmt.Fprintf(&b, "\n# github mcp config (%s; token redacted):\n", state)
	b.WriteString(indentJSON(mcpJSON))
	b.WriteString("\n")
	return b.String()
}

// indentJSON pretty-prints compact JSON; on any error it returns the input
// unchanged so display never fails.
func indentJSON(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}
