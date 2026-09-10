// agent-sandbox/cmd/debug.go
package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/claude"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/envflag"
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
	envKeys, err := envflag.Load(opts.EnvRefs)
	if err != nil {
		return err
	}
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	// Agent section, matching cmd/claude.go: --env is for the launched agent.
	cfg.Sandbox.Agent.AllowEnv = append(cfg.Sandbox.Agent.AllowEnv, envKeys...)

	r, err := sandboxhost.Resolve(cfg, "claude")
	if err != nil {
		return err
	}
	profilePath, cleanupProfile, err := r.WriteProfile()
	if err != nil {
		return err
	}
	defer cleanupProfile()

	// debug exists to show the exact invocation the launcher builds, so it must
	// include the broker socket grant; passing "" here would hide the only thing
	// the command broker adds to the wrap invocation.
	brokerSocket, err := claude.BrokerSocketPath()
	if err != nil {
		return err
	}

	_, nonoArgs, err := claude.BuildArgs(cfg, opts, "", profilePath, brokerSocket)
	if err != nil {
		return err
	}
	fmt.Println(strings.Join(nonoArgs, " "))

	selfPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate agent-sandbox: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "command broker:")
	fmt.Fprintln(cmd.OutOrStdout(), "  "+strings.Join(
		claude.BrokerArgs(cfg, nonoPathForDisplay(), selfPath, brokerSocket, cwd), " "))

	profileJSON, err := r.ProfileJSON()
	if err != nil {
		return err
	}
	mcpJSON, err := claude.RedactedGithubMCPConfigJSON()
	if err != nil {
		return err
	}
	fmt.Print(formatGeneratedConfigs(profilePath, profileJSON, claude.GithubMCPEnabled(), mcpJSON))
	return nil
}

// nonoPathForDisplay resolves nono the same way the launcher does, so debug
// prints the binary that will actually run rather than an unresolved literal.
// It falls back to "nono" when lookup fails: debug must still print something
// useful when nono is missing, which is itself worth being able to see here.
func nonoPathForDisplay() string {
	if path, err := exec.LookPath("nono"); err == nil {
		return path
	}
	return "nono"
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
