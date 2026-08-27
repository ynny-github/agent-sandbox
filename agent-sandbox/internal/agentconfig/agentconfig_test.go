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
	cfg.Sandbox.Agent.AllowCommands = []string{"git *"}
	cfg.Sandbox.Agent.DropCommands = []config.DropRule{
		{Pattern: "git push --force*"},
		{Pattern: "gh *", Message: "gh is disabled; use the GitHub MCP tools."},
	}

	got := agentconfig.Explain(cfg)
	for _, want := range []string{
		"# agent-sandbox environment",
		"Bash/Monitor tools",
		"PreToolUse hook",
		"returned inline in the tool result",
		"## Commands that run on the host (allow)",
		"- git *",
		"## Refused commands (drop)",
		"- git push --force*",
		"- gh * — gh is disabled; use the GitHub MCP tools.",
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
	got := agentconfig.Explain(cfg)
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

func TestExplain_FilesystemSection(t *testing.T) {
	cfg := &config.Config{ToolMode: "hook"}
	got := agentconfig.Explain(cfg)
	for _, want := range []string{
		"## Filesystem: the same paths everywhere",
		"no bind mount",
		"HOME keeps its real host value",
		"`[sandbox.shared]` (shared with the agent) or `[sandbox.shell]`",
		"Anything declared under `[sandbox.agent]`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing filesystem note %q\nfull output:\n%s", want, got)
		}
	}
}

func TestExplain_SafeWrappers(t *testing.T) {
	cfg := &config.Config{ToolMode: "hook"}
	got := agentconfig.Explain(cfg,
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
	if got := agentconfig.Explain(cfg); strings.Contains(got, "Safe command wrappers") {
		t.Errorf("Explain() should omit the safe wrappers section when none are passed:\n%s", got)
	}
}

func TestExplain_NoExtraDomainsByDefault(t *testing.T) {
	cfg := &config.Config{ToolMode: "mcp"}
	got := agentconfig.Explain(cfg)
	if !strings.Contains(got, "No extra domains beyond the developer profile.") {
		t.Errorf("Explain() should render no extra domains by default:\n%s", got)
	}
}

func TestExplain_AllowDomainsListed(t *testing.T) {
	cfg := &config.Config{ToolMode: "mcp"}
	cfg.Sandbox.Shell.AllowDomains = []string{"proxy.golang.org", "sum.golang.org"}
	got := agentconfig.Explain(cfg)
	for _, want := range []string{"proxy.golang.org", "sum.golang.org"} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing allow domain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "No extra domains") {
		t.Errorf("Explain() should not claim no extra domains when AllowDomains is set:\n%s", got)
	}
}

// The domain list an agent reads must be the resolved one: a capability can
// carry domains that never appear in [sandbox.shell] allow_domains.
func TestExplain_CapabilityDomainsListed(t *testing.T) {
	cfg := &config.Config{ToolMode: "mcp"}
	cfg.Sandbox.Shared.Capabilities = []string{"go"}
	got := agentconfig.Explain(cfg)
	if !strings.Contains(got, "proxy.golang.org") {
		t.Errorf("Explain() missing the go capability's domains:\n%s", got)
	}
	if strings.Contains(got, "No extra domains") {
		t.Errorf("Explain() should not claim no extra domains when a capability adds some:\n%s", got)
	}
}

// The filesystem section names the paths a sandboxed command actually reaches,
// resolved from the config. Describing the sections alone would leave the agent
// guessing, since which grants apply depends on where they were written.
func TestExplain_ResolvedOutsidePaths(t *testing.T) {
	cfg := &config.Config{ToolMode: "hook"}
	cfg.Sandbox.Shared.Capabilities = []string{"mise"}
	cfg.Sandbox.Shell.Allow = []string{"/srv/cache"}
	cfg.Sandbox.Agent.Capabilities = []string{"ssh"}

	got := agentconfig.Explain(cfg)
	for _, want := range []string{
		"- `/srv/cache` (read+write)",
		"- `~/.config/mise` (read-only)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing resolved path %q\nfull output:\n%s", want, got)
		}
	}
	// The agent's own credential grant must not be listed as reachable.
	if strings.Contains(got, "~/.ssh") {
		t.Errorf("Explain() lists an [sandbox.agent] grant as command-reachable:\n%s", got)
	}
}

func TestExplain_NoOutsidePaths(t *testing.T) {
	got := agentconfig.Explain(&config.Config{ToolMode: "hook"})
	if !strings.Contains(got, "nothing outside the working directory") {
		t.Errorf("Explain() should say so when no grant reaches outside the working directory:\n%s", got)
	}
}
