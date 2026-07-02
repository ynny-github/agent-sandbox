package cmd

import (
	"encoding/json"
	"testing"
)

func TestBuildHookSettingsJSON(t *testing.T) {
	got, err := buildHookSettingsJSON()
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
