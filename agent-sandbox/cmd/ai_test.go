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

func runConfigCheckWith(t *testing.T, body string) (string, error) {
	t.Helper()
	orig := configPath
	configPath = writeTempConfig(t, body)
	t.Cleanup(func() { configPath = orig })

	var buf bytes.Buffer
	configCheckCmd.SetOut(&buf)
	err := runConfigCheck(configCheckCmd, nil)
	return buf.String(), err
}

func TestRunConfigCheck_ValidConfig(t *testing.T) {
	out, err := runConfigCheckWith(t, `
tool_mode = "hook"

[sandbox.shared]
capabilities = ["go"]

[sandbox.agent]
allow_commands = ["go *"]
`)
	if err != nil {
		t.Fatalf("runConfigCheck: %v", err)
	}
	if !strings.Contains(out, "proxy.golang.org") {
		t.Errorf("output does not summarise the resolved domains:\n%s", out)
	}
}

func TestRunConfigCheck_BrokenToml(t *testing.T) {
	if _, err := runConfigCheckWith(t, "tool_mode = \n"); err == nil {
		t.Fatal("expected an error for unparseable TOML, got nil")
	}
}

// An unknown capability name passes config.Load — capability names are only
// resolved in sandboxhost — so without the resolve step it would surface at the
// next launch instead of here.
func TestRunConfigCheck_UnknownCapability(t *testing.T) {
	_, err := runConfigCheckWith(t, `
tool_mode = "hook"

[sandbox.shared]
capabilities = ["gooo"]
`)
	if err == nil {
		t.Fatal("expected an error for an unknown capability, got nil")
	}
	if !strings.Contains(err.Error(), "gooo") {
		t.Errorf("error does not name the offending capability: %v", err)
	}
}
