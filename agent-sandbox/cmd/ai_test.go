package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "agent-sandbox.toml")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunExplain_RendersConfig(t *testing.T) {
	cfgPath := writeTempConfig(t, `
tool_mode = "hook"

[mcp]
command_output_dir = "./tmp"

[sandbox.shell]
allow_domains = ["proxy.golang.org"]

[sandbox.agent]
allow_commands = ["git *", "go *"]
drop_commands = [{ pattern = "git push --force*" }]
`)
	orig := configPath
	configPath = cfgPath
	t.Cleanup(func() { configPath = orig })

	var buf bytes.Buffer
	explainCmd.SetOut(&buf)
	if err := runExplain(explainCmd, nil); err != nil {
		t.Fatalf("runExplain: %v", err)
	}
	for _, want := range []string{
		"# agent-sandbox environment",
		"- git *",
		"- go *",
		"- git push --force*",
		"proxy.golang.org",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n%s", want, buf.String())
		}
	}
}

func TestRunExplain_MissingConfig(t *testing.T) {
	orig := configPath
	configPath = "/nonexistent/path.toml"
	t.Cleanup(func() { configPath = orig })
	if err := runExplain(explainCmd, nil); err == nil {
		t.Fatal("expected config error, got nil")
	}
}
