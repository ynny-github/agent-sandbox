package agentconfig_test

import (
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/agentconfig"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

func TestPointer_MentionsExplainCommand(t *testing.T) {
	got := agentconfig.Pointer()
	if !strings.Contains(got, "agent-sandbox ai explain") {
		t.Errorf("Pointer() missing command reference:\n%s", got)
	}
}

func TestExplain_HookMode(t *testing.T) {
	cfg := &config.Config{ToolMode: "hook"}
	cfg.Sandbox.Command.Allow = []string{"git *"}
	cfg.Sandbox.Command.Drop = []string{"git push --force*"}
	cfg.Sandbox.Container.Image = "sandbox:0.1.0"

	got := agentconfig.Explain(cfg)
	for _, want := range []string{
		"# agent-sandbox environment",
		`tool_mode = "hook"`,
		"## Commands that run on the host (allow)",
		"- git *",
		"## Refused commands (drop)",
		"- git push --force*",
		"sandbox:0.1.0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestExplain_McpMode(t *testing.T) {
	cfg := &config.Config{ToolMode: "mcp"}
	got := agentconfig.Explain(cfg)
	if !strings.Contains(got, "run_command") {
		t.Errorf("Explain() mcp branch missing run_command:\n%s", got)
	}
}

func TestExplain_EmptyListsRenderNone(t *testing.T) {
	cfg := &config.Config{ToolMode: "mcp"}
	got := agentconfig.Explain(cfg)
	if !strings.Contains(got, "- (none)") {
		t.Errorf("Explain() should render (none) for empty allow/drop:\n%s", got)
	}
	if !strings.Contains(got, "- network: none") {
		t.Errorf("Explain() should render network: none when unset:\n%s", got)
	}
}

func TestExplain_NetworkListed(t *testing.T) {
	cfg := &config.Config{ToolMode: "hook"}
	cfg.Sandbox.Network.AllowHosts = []string{"example.com", "api.example.com"}
	cfg.Sandbox.Network.AllowCIDRs = []string{"10.0.0.0/8"}

	got := agentconfig.Explain(cfg)
	for _, want := range []string{
		"- network allow_hosts: example.com, api.example.com",
		"- network allow_cidrs: 10.0.0.0/8",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing %q\nfull output:\n%s", want, got)
		}
	}
	if strings.Contains(got, "network: none") {
		t.Errorf("Explain() should not render network: none when hosts/cidrs are set:\n%s", got)
	}
}
