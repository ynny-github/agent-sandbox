package agentconfig_test

import (
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/agentconfig"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxhost"
)

func TestPointer_MentionsExplainCommand(t *testing.T) {
	got := agentconfig.Pointer()
	if !strings.Contains(got, "agent-sandbox ai explain") {
		t.Errorf("Pointer() missing command reference:\n%s", got)
	}
}

func TestExplain_HookMode(t *testing.T) {
	cfg := &config.Config{ToolMode: "hook"}
	got := agentconfig.Explain(cfg, "agent-sandbox.toml")
	for _, want := range []string{
		"# agent-sandbox environment",
		"Bash/Monitor tools",
		"PreToolUse hook",
		"returned inline in the tool result",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing %q\nfull output:\n%s", want, got)
		}
	}
	if strings.Contains(got, "run_command") {
		t.Errorf("Explain() hook branch should not mention the mcp run_command tool:\n%s", got)
	}
}

func TestExplain_McpMode(t *testing.T) {
	cfg := &config.Config{ToolMode: "mcp"}
	got := agentconfig.Explain(cfg, "agent-sandbox.toml")
	for _, want := range []string{
		"run_command",
		"written to files",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() mcp branch missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "PreToolUse hook") {
		t.Errorf("Explain() mcp branch should not mention the hook flow:\n%s", got)
	}
}

// Explain names the command profile the broker runs under, resolved the same
// way config.Config.CommandProfilePath() does, without describing what it
// allows: agent-sandbox neither generates nor reads its contents.
func TestExplain_NamesTheCommandProfilePath(t *testing.T) {
	cfg := &config.Config{ToolMode: "hook"}
	got := agentconfig.Explain(cfg, "agent-sandbox.toml")
	if !strings.Contains(got, cfg.CommandProfilePath()) {
		t.Errorf("Explain() missing the command profile path %q\nfull output:\n%s", cfg.CommandProfilePath(), got)
	}
}

func TestExplain_SafeWrappers(t *testing.T) {
	cfg := &config.Config{ToolMode: "hook"}
	got := agentconfig.Explain(cfg, "agent-sandbox.toml",
		agentconfig.SafeCommand{Use: "git [args...]", Short: "Run git, refusing known-dangerous invocations"},
		agentconfig.SafeCommand{Use: "docker-compose [args...]", Short: "Run docker compose only after safety validation"},
	)
	for _, want := range []string{
		"## Safe command wrappers",
		"`agent-sandbox safe git [args...]` — Run git, refusing known-dangerous invocations",
		"`agent-sandbox safe docker-compose [args...]` — Run docker compose only after safety validation",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestExplain_NoSafeWrappersSection_WhenNone(t *testing.T) {
	cfg := &config.Config{ToolMode: "hook"}
	if got := agentconfig.Explain(cfg, "agent-sandbox.toml"); strings.Contains(got, "Safe command wrappers") {
		t.Errorf("Explain() should omit the safe wrappers section when none are passed:\n%s", got)
	}
}

func TestExplain_ConfigEditingSection(t *testing.T) {
	cfg := &config.Config{ToolMode: "hook"}
	got := agentconfig.Explain(cfg, "/work/proj/agent-sandbox.toml")

	for _, want := range []string{
		"## Changing the config",
		"/work/proj/agent-sandbox.toml",
		"[sandbox.agent]",
		"tool_mode",
		"capabilities",
		"agent-sandbox ai config-check",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() config section missing %q\nfull output:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"[sandbox.shared]", "[sandbox.shell]", "allow_commands", "drop_commands", "allow_domains"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("Explain() config section still mentions removed key %q\nfull output:\n%s", unwanted, got)
		}
	}
}

func TestExplain_ListsCapabilityNames(t *testing.T) {
	got := agentconfig.Explain(&config.Config{ToolMode: "hook"}, "agent-sandbox.toml")
	for _, name := range sandboxhost.CapabilityNames() {
		if !strings.Contains(got, "`"+name+"`") {
			t.Errorf("Explain() does not list capability %q\nfull output:\n%s", name, got)
		}
	}
}

func TestExplain_SaysEditsApplyAtNextLaunch(t *testing.T) {
	got := agentconfig.Explain(&config.Config{ToolMode: "hook"}, "agent-sandbox.toml")
	for _, want := range []string{
		"does not affect the current session",
		"agent-sandbox claude",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing %q about when edits take effect\nfull output:\n%s", want, got)
		}
	}
}

func TestExplain_MentionsUserScopeConfigMerge(t *testing.T) {
	got := agentconfig.Explain(&config.Config{ToolMode: "hook"}, "agent-sandbox.toml")
	for _, want := range []string{
		"~/.config/agent-sandbox/config.toml",
		"union",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing %q about the user-scope config merge\nfull output:\n%s", want, got)
		}
	}
}
