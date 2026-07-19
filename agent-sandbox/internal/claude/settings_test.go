package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHookSettingsJSON(t *testing.T) {
	got, err := settingsJSON("", "", true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var settings map[string]any
	if err := json.Unmarshal([]byte(got), &settings); err != nil {
		t.Fatalf("result is not valid JSON: %v (%q)", err, got)
	}
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("missing hooks object in %v", settings)
	}
	entries, ok := hooks["PreToolUse"].([]any)
	if !ok {
		t.Fatalf("missing PreToolUse array in %v", hooks)
	}

	var got2 []string
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("entry not an object: %v", e)
		}
		matcher, _ := m["matcher"].(string)
		got2 = append(got2, matcher)
		inner, ok := m["hooks"].([]any)
		if !ok || len(inner) != 1 {
			t.Fatalf("entry %q missing single hook: %v", matcher, m)
		}
		hm, _ := inner[0].(map[string]any)
		if hm["type"] != "command" || hm["command"] != "agent-sandbox hook" {
			t.Errorf("entry %q hook = %v, want type=command command=agent-sandbox hook", matcher, hm)
		}
	}
	if len(got2) != 2 || got2[0] != "Bash" || got2[1] != "Monitor" {
		t.Errorf("matchers = %v, want [Bash Monitor]", got2)
	}
}

func TestHookSettingsJSON_WithPolicyFile(t *testing.T) {
	got, err := settingsJSON("/state/policy-1.json", "", true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "agent-sandbox hook --policy-file '/state/policy-1.json'") {
		t.Errorf("settings missing policy-file hook command; got %q", got)
	}
}

func TestSettingsJSON_DenyRuleForMCPPath(t *testing.T) {
	got, err := settingsJSON("", "/tmp/asb-mcp-1.json", false, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(got, `"deny"`) || !strings.Contains(got, "Read(//tmp/asb-mcp-1.json)") {
		t.Errorf("settings missing deny rule for the mcp path; got %q", got)
	}
	if strings.Contains(got, "PreToolUse") {
		t.Errorf("non-hook settings should not contain hooks; got %q", got)
	}
}

func TestSettingsJSON_HookAndDenyCombined(t *testing.T) {
	got, err := settingsJSON("/state/p.json", "/tmp/asb-mcp-1.json", true, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(got, "PreToolUse") {
		t.Errorf("hook mode should include PreToolUse; got %q", got)
	}
	if !strings.Contains(got, "Read(//tmp/asb-mcp-1.json)") {
		t.Errorf("should include the deny rule; got %q", got)
	}
}

func TestSettingsJSON_EmptyWhenNothing(t *testing.T) {
	got, err := settingsJSON("", "", false, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty settings, got %q", got)
	}
}

func TestDenyReadRule(t *testing.T) {
	if r := denyReadRule("/tmp/x.json"); r != "Read(//tmp/x.json)" {
		t.Errorf("denyReadRule = %q, want Read(//tmp/x.json)", r)
	}
}

func TestSettingsJSON_DenyRulesFromCapabilities(t *testing.T) {
	got, err := settingsJSON("", "", false, []string{"Read(~/.ssh/**)", "Grep(~/.ssh/**)"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(got, `"deny"`) ||
		!strings.Contains(got, "Read(~/.ssh/**)") ||
		!strings.Contains(got, "Grep(~/.ssh/**)") {
		t.Errorf("expected capability deny rules; got %q", got)
	}
}

func TestSettingsJSON_MergesCapabilityAndMCPDeny(t *testing.T) {
	got, err := settingsJSON("", "/tmp/asb-mcp-1.json", false, []string{"Read(~/.ssh/**)"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(got, "Read(~/.ssh/**)") || !strings.Contains(got, "Read(//tmp/asb-mcp-1.json)") {
		t.Errorf("expected both capability and mcp deny rules; got %q", got)
	}
}

func TestSettingsJSON_EmptyWhenNoDenyNoHook(t *testing.T) {
	got, err := settingsJSON("", "", false, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty settings; got %q", got)
	}
}

func TestSettingsJSON_BlocksGithubRepoWritesWhenMCPActive(t *testing.T) {
	got, err := settingsJSON("", "/tmp/asb-mcp-1.json", false, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	for _, tool := range []string{
		"mcp__github__create_or_update_file",
		"mcp__github__delete_file",
		"mcp__github__push_files",
		"mcp__github__create_branch",
		"mcp__github__create_repository",
		"mcp__github__fork_repository",
	} {
		if !strings.Contains(got, tool) {
			t.Errorf("expected deny rule for %s; got %q", tool, got)
		}
	}
}

func TestSettingsJSON_NoGithubDenyWithoutMCP(t *testing.T) {
	got, err := settingsJSON("/state/p.json", "", true, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if strings.Contains(got, "mcp__github__") {
		t.Errorf("no github write deny expected when MCP config absent; got %q", got)
	}
}
