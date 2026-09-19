package agentconfig_test

import (
	"os"
	"path/filepath"
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

func TestExplain_RoutingSection(t *testing.T) {
	cfg := &config.Config{}
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
	// The MCP tool is gone; a document that still offered it would send the
	// agent after a tool its session does not have.
	if strings.Contains(got, "run_command") {
		t.Errorf("Explain() must not mention the removed run_command MCP tool:\n%s", got)
	}
}

// Explain names the command profile the broker runs under, resolved the same
// way config.Config.CommandProfilePath() does, without describing what it
// allows: agent-sandbox neither generates nor reads its contents.
func TestExplain_NamesTheCommandProfilePath(t *testing.T) {
	cfg := &config.Config{}
	got := agentconfig.Explain(cfg, "agent-sandbox.toml")
	if !strings.Contains(got, cfg.CommandProfilePath()) {
		t.Errorf("Explain() missing the command profile path %q\nfull output:\n%s", cfg.CommandProfilePath(), got)
	}
}

func TestExplain_ConfigEditingSection(t *testing.T) {
	cfg := &config.Config{}
	got := agentconfig.Explain(cfg, "/work/proj/agent-sandbox.toml")

	for _, want := range []string{
		"## Changing the config",
		"/work/proj/agent-sandbox.toml",
		"[agents.claude]",
		"command_profile",
		"agent-sandbox ai config-check",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() config section missing %q\nfull output:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"[sandbox.shared]", "[sandbox.shell]", "[sandbox.agent]", "capabilities", "allow_commands", "drop_commands", "allow_domains", "tool_mode", "[mcp]"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("Explain() config section still mentions removed key %q\nfull output:\n%s", unwanted, got)
		}
	}
}

func TestExplain_SaysEditsApplyAtNextLaunch(t *testing.T) {
	got := agentconfig.Explain(&config.Config{}, "agent-sandbox.toml")
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
	got := agentconfig.Explain(&config.Config{}, "agent-sandbox.toml")
	for _, want := range []string{
		"~/.config/agent-sandbox/config.toml",
		"wins",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing %q about the user-scope config merge\nfull output:\n%s", want, got)
		}
	}
}
func TestExplain_NamesBothProfilesAndTheNonoCommands(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"command-profile.json", "claude-profile.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	out := agentconfig.Explain(cfg, cfgPath)
	for _, want := range []string{
		filepath.Join(dir, "claude-profile.json"),
		filepath.Join(dir, "command-profile.json"),
		"nono profile show",
		"nono why",
		"[agents.claude]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"capabilities = []", "[sandbox.agent]"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("explain output still mentions %q\n%s", unwanted, out)
		}
	}
}

// TestExplain_PointsAtNonoRatherThanListingCommands is the whole point of the
// rewrite: the document must teach the agent how to ask nono, and must not
// restate the profile's contents.
func TestExplain_PointsAtNonoRatherThanListingCommands(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{CommandProfile: "command-profile.json"}
	out := agentconfig.Explain(cfg, filepath.Join(dir, "agent-sandbox.toml"))

	for _, want := range []string{
		"nono why --profile",
		"--command <name>",
		"--caller <name>",
		"nono profile show",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output does not mention %q:\n%s", want, out)
		}
	}
}

// TestExplain_MakesNoTwoTierOrWrapperClaims guards the two false statements the
// old template carried: that git routes through a "safe git" wrapper, and that
// a program in neither tier is never dispatched. Neither is true under the
// 2026-09-12 profile.
func TestExplain_MakesNoTwoTierOrWrapperClaims(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{CommandProfile: "command-profile.json"}
	out := agentconfig.Explain(cfg, filepath.Join(dir, "agent-sandbox.toml"))

	for _, forbidden := range []string{
		"safe git",
		"safe <tool>",
		"realgit",
		"wrapper",
		"This allowlist is absolute",
		"never dispatched by the broker",
	} {
		if strings.Contains(out, forbidden) {
			t.Errorf("explain output still claims %q:\n%s", forbidden, out)
		}
	}
}

// TestExplain_ReadsNoProfile proves the delegation rule mechanically: the
// command profile named by the config does not exist, and Explain must still
// produce its full document rather than degrade or report a broker issue.
func TestExplain_ReadsNoProfile(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{CommandProfile: "does-not-exist.json"}
	out := agentconfig.Explain(cfg, filepath.Join(dir, "agent-sandbox.toml"))

	if strings.Contains(out, "could not identify the broker entry") {
		t.Errorf("explain still reports a broker issue from reading the profile:\n%s", out)
	}
	if !strings.Contains(out, "Changing the config") {
		t.Errorf("explain did not render its full document:\n%s", out)
	}
	if !strings.Contains(out, "does-not-exist.json") {
		t.Errorf("explain did not name the missing profile path it was pointed at:\n%s", out)
	}
}
