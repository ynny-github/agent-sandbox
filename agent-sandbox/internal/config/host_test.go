package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTOML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "agent-sandbox.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// baseProject is the minimum required config; host tests append a [sandbox.host].
const baseProject = `
tool_mode = "mcp"
[mcp]
command_output_dir = "/tmp/out"
`

func TestLoad_DecodesHostSection(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from any real user config
	p := writeTOML(t, baseProject+`
[sandbox.host]
capabilities = ["go", "ssh"]
read = ["~/.orbstack"]
allow_env = ["FOO", "BAR*"]
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Sandbox.Host.Capabilities; len(got) != 2 || got[0] != "go" || got[1] != "ssh" {
		t.Errorf("Capabilities = %v", got)
	}
	if got := cfg.Sandbox.Host.Read; len(got) != 1 || got[0] != "~/.orbstack" {
		t.Errorf("Read = %v", got)
	}
	if got := cfg.Sandbox.Host.AllowEnv; len(got) != 2 || got[0] != "FOO" || got[1] != "BAR*" {
		t.Errorf("AllowEnv = %v", got)
	}
}

func TestLoad_UnionsHostListsAcrossScopes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userDir := filepath.Join(home, ".config", "agent-sandbox")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "config.toml"), []byte(`
[sandbox.host]
capabilities = ["go"]
read = ["~/.a"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := writeTOML(t, baseProject+`
[sandbox.host]
capabilities = ["python", "go"]
read = ["~/.b"]
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// user-first order, de-duped: go (user) then python (project).
	if got := cfg.Sandbox.Host.Capabilities; len(got) != 2 || got[0] != "go" || got[1] != "python" {
		t.Errorf("Capabilities = %v, want [go python]", got)
	}
	if got := cfg.Sandbox.Host.Read; len(got) != 2 || got[0] != "~/.a" || got[1] != "~/.b" {
		t.Errorf("Read = %v, want [~/.a ~/.b]", got)
	}
}
