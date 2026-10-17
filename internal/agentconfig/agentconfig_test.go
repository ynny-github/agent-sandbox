package agentconfig_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/internal/agentconfig"
)

func TestPointer_MentionsExplainCommand(t *testing.T) {
	got := agentconfig.Pointer()
	if !strings.Contains(got, "agent-sandbox ai explain") {
		t.Errorf("Pointer() missing command reference:\n%s", got)
	}
}

func TestExplain_RoutingSection(t *testing.T) {
	got := agentconfig.Explain(cmdProfile, agentProfile)
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

const (
	cmdProfile   = ".agent-sandbox/command-profile.json"
	agentProfile = ".agent-sandbox/claude-profile.json"
)

// Explain names both profiles without describing what they allow:
// agent-sandbox neither generates nor reads their contents.
func TestExplain_NamesBothProfiles(t *testing.T) {
	got := agentconfig.Explain(cmdProfile, agentProfile)
	for _, want := range []string{cmdProfile, agentProfile} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestExplain_ChangingTheSandboxSection(t *testing.T) {
	got := agentconfig.Explain(cmdProfile, agentProfile)
	for _, want := range []string{"## Changing the sandbox", "agent-sandbox launch", "agent-sandbox doctor"} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing %q\nfull output:\n%s", want, got)
		}
	}
	for _, gone := range []string{"agent-sandbox.toml", "config-check", "agent-sandbox claude", "[agents.claude]"} {
		if strings.Contains(got, gone) {
			t.Errorf("Explain() still mentions %q\nfull output:\n%s", gone, got)
		}
	}
}

func TestExplain_SaysEditsApplyAtNextLaunch(t *testing.T) {
	got := agentconfig.Explain(cmdProfile, agentProfile)
	for _, want := range []string{
		"does not affect the current session",
		"agent-sandbox launch",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Explain() missing %q about when edits take effect\nfull output:\n%s", want, got)
		}
	}
}

func TestExplain_NamesBothProfilesAndTheNonoCommands(t *testing.T) {
	dir := t.TempDir()
	out := agentconfig.Explain(filepath.Join(dir, "command-profile.json"), filepath.Join(dir, "claude-profile.json"))
	for _, want := range []string{
		filepath.Join(dir, "claude-profile.json"),
		filepath.Join(dir, "command-profile.json"),
		"nono profile show",
		"nono why",
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
	out := agentconfig.Explain(filepath.Join(dir, "command-profile.json"), agentProfile)

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
	out := agentconfig.Explain(filepath.Join(dir, "command-profile.json"), agentProfile)

	for _, forbidden := range []string{
		"safe git",
		"safe <tool>",
		"realgit",
		"wrapper",
		"This allowlist is absolute",
		"never dispatched by execd",
	} {
		if strings.Contains(out, forbidden) {
			t.Errorf("explain output still claims %q:\n%s", forbidden, out)
		}
	}
}

// TestExplain_ReadsNoProfile proves the delegation rule mechanically: the
// command profile named by the config does not exist, and Explain must still
// produce its full document rather than degrade or report an execd issue.
func TestExplain_ReadsNoProfile(t *testing.T) {
	dir := t.TempDir()
	out := agentconfig.Explain(filepath.Join(dir, "does-not-exist.json"), agentProfile)

	if strings.Contains(out, "could not identify the execd entry") {
		t.Errorf("explain still reports an execd issue from reading the profile:\n%s", out)
	}
	if !strings.Contains(out, "Changing the sandbox") {
		t.Errorf("explain did not render its full document:\n%s", out)
	}
	if !strings.Contains(out, "does-not-exist.json") {
		t.Errorf("explain did not name the missing profile path it was pointed at:\n%s", out)
	}
}
