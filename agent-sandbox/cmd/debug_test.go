package cmd

import (
	"strings"
	"testing"
)

func TestRunDebug_MissingConfig(t *testing.T) {
	orig := configPath
	configPath = "/nonexistent/path.toml"
	t.Cleanup(func() { configPath = orig })
	if err := runDebug(debugCmd, nil); err == nil {
		t.Fatal("expected config error, got nil")
	}
}

func TestFormatGeneratedConfigs_ProfileAndEnabledMCP(t *testing.T) {
	profile := []byte(`{"extends":"claude"}`)
	mcp := []byte(`{"mcpServers":{"github":{"env":{"GITHUB_PERSONAL_ACCESS_TOKEN":"***redacted***"}}}}`)
	out := formatGeneratedConfigs("/tmp/p.json", profile, true, mcp)
	for _, want := range []string{
		"# generated nono profile (/tmp/p.json):",
		`"extends": "claude"`, // pretty-printed (indented)
		"# github mcp config (enabled; token redacted):",
		"***redacted***",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

func TestFormatGeneratedConfigs_MCPDisabledLabel(t *testing.T) {
	out := formatGeneratedConfigs("/tmp/p.json", []byte(`{"a":1}`), false, []byte(`{"b":2}`))
	if !strings.Contains(out, "# github mcp config (disabled; token redacted):") {
		t.Errorf("expected disabled label; got:\n%s", out)
	}
}
