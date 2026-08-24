// agent-sandbox/cmd/debug.go
package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/claude"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/envflag"
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
	envKeys, err := envflag.Load(opts.EnvRefs)
	if err != nil {
		return err
	}
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	// Agent section, matching cmd/claude.go: --env is for the launched agent.
	cfg.Sandbox.Agent.Host.AllowEnv = append(cfg.Sandbox.Agent.Host.AllowEnv, envKeys...)

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

	// debug exists to show the exact invocation the launcher builds, so it must
	// include the broker socket grant; passing "" here would hide the only thing
	// the command broker adds to the wrap invocation.
	brokerSocket, err := claude.BrokerSocketPath()
	if err != nil {
		return err
	}

	_, nonoArgs, err := claude.BuildArgs(cfg, opts, snapshotPath, "", profilePath, r.DenyRules, brokerSocket)
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
	fmt.Print(formatGeneratedConfigs(profilePath, profileJSON, claude.GithubMCPEnabled(), mcpJSON))

	// The command profile is the other half of the policy: same expansion, fed by
	// [sandbox.host] + [sandbox.command.host] instead of [sandbox.agent.host].
	// Printing both is what makes the split inspectable — the difference between
	// them is exactly what the two sections declare.
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	cmdResolved, err := sandboxhost.ResolveCommand(cfg, cwd)
	if err != nil {
		return err
	}
	cmdJSON, err := cmdResolved.ProfileJSON()
	if err != nil {
		return err
	}
	fmt.Print(formatCommandProfile(cmdJSON, cmdResolved.ProtectedGrants()))
	return nil
}

// formatCommandProfile renders the per-command nono profile, warning when it
// grants a protected path. Raw grants cannot produce one, so such a path always
// comes from a credential capability (docker/ssh) declared in the shared
// [sandbox.host] instead of [sandbox.agent.host] — host keys reachable from any
// sandboxed command, which is rarely what the author meant.
func formatCommandProfile(profileJSON []byte, protected []string) string {
	var b strings.Builder
	b.WriteString("\n# generated nono profile for brokered commands:\n")
	b.WriteString(indentJSON(profileJSON))
	b.WriteString("\n")
	if len(protected) > 0 {
		fmt.Fprintf(&b, "\n# warning: brokered commands can read %s\n", strings.Join(protected, ", "))
		b.WriteString("#          move the capability granting it to [sandbox.agent.host]\n")
	}
	return b.String()
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
