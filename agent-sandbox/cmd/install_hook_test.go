package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readSettingsFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	return m
}

func preToolUseEntries(t *testing.T, settings map[string]any) []any {
	t.Helper()
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("missing hooks object in %v", settings)
	}
	entries, ok := hooks["PreToolUse"].([]any)
	if !ok {
		t.Fatalf("missing PreToolUse array in %v", hooks)
	}
	return entries
}

func matchers(entries []any) []string {
	var out []string
	for _, e := range entries {
		if m, ok := e.(map[string]any); ok {
			if s, ok := m["matcher"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func TestRunInstallHookIn_CreatesSettings(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := runInstallHookIn(dir, &out); err != nil {
		t.Fatalf("runInstallHookIn error: %v", err)
	}

	settings := readSettingsFile(t, filepath.Join(dir, ".claude", "settings.json"))
	got := matchers(preToolUseEntries(t, settings))
	if len(got) != 2 || got[0] != "Bash" || got[1] != "Monitor" {
		t.Errorf("matchers = %v, want [Bash Monitor]", got)
	}
}

func TestRunInstallHookIn_Idempotent(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := runInstallHookIn(dir, &out); err != nil {
		t.Fatalf("first install error: %v", err)
	}
	if err := runInstallHookIn(dir, &out); err != nil {
		t.Fatalf("second install error: %v", err)
	}

	settings := readSettingsFile(t, filepath.Join(dir, ".claude", "settings.json"))
	got := matchers(preToolUseEntries(t, settings))
	if len(got) != 2 {
		t.Errorf("matchers after two installs = %v, want exactly 2 (no duplicates)", got)
	}
}

func TestHookInstalledInSettings(t *testing.T) {
	both := map[string]any{}
	ensurePreToolUseHook(both, "Bash", hookCommand)
	ensurePreToolUseHook(both, "Monitor", hookCommand)

	onlyBash := map[string]any{}
	ensurePreToolUseHook(onlyBash, "Bash", hookCommand)

	required := []string{"Bash", "Monitor"}

	if !hookInstalledInSettings(both, hookCommand, required) {
		t.Error("both Bash and Monitor installed: want true")
	}
	if hookInstalledInSettings(onlyBash, hookCommand, required) {
		t.Error("only Bash installed: want false")
	}
	if hookInstalledInSettings(map[string]any{}, hookCommand, required) {
		t.Error("empty settings: want false")
	}
	if hookInstalledInSettings(both, "some-other-command", required) {
		t.Error("different command: want false")
	}
}

func TestRunInstallHookIn_PreservesExistingSettings(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	existing := `{
  "model": "opus",
  "hooks": {
    "PreToolUse": [
      { "matcher": "Edit", "hooks": [ { "type": "command", "command": "echo edit" } ] }
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runInstallHookIn(dir, &out); err != nil {
		t.Fatalf("runInstallHookIn error: %v", err)
	}

	settings := readSettingsFile(t, filepath.Join(claudeDir, "settings.json"))
	if settings["model"] != "opus" {
		t.Errorf("model = %v, want opus (existing key must be preserved)", settings["model"])
	}
	got := matchers(preToolUseEntries(t, settings))
	if len(got) != 3 || got[0] != "Edit" || got[1] != "Bash" || got[2] != "Monitor" {
		t.Errorf("matchers = %v, want [Edit Bash Monitor]", got)
	}
}
