package config

import (
	"os"
	"path/filepath"
	"slices"
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

func TestLoad_DecodesPerSideHostSections(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from any real user config
	p := writeTOML(t, baseProject+`
[sandbox.host]
capabilities = ["go"]

[sandbox.agent.host]
capabilities = ["ssh"]
read = ["~/notes"]

[sandbox.command.host]
capabilities = ["node"]
allow_env = ["CI"]
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Each section stands on its own: Load never copies grants between them.
	if got := cfg.Sandbox.Host.Capabilities; len(got) != 1 || got[0] != "go" {
		t.Errorf("Host.Capabilities = %v, want [go]", got)
	}
	if got := cfg.Sandbox.Agent.Host.Capabilities; len(got) != 1 || got[0] != "ssh" {
		t.Errorf("Agent.Host.Capabilities = %v, want [ssh]", got)
	}
	if got := cfg.Sandbox.Agent.Host.Read; len(got) != 1 || got[0] != "~/notes" {
		t.Errorf("Agent.Host.Read = %v, want [~/notes]", got)
	}
	if got := cfg.Sandbox.Command.Host.Capabilities; len(got) != 1 || got[0] != "node" {
		t.Errorf("Command.Host.Capabilities = %v, want [node]", got)
	}
	if got := cfg.Sandbox.Command.Host.AllowEnv; len(got) != 1 || got[0] != "CI" {
		t.Errorf("Command.Host.AllowEnv = %v, want [CI]", got)
	}
}

// The user/project union covers all three host sections, not just the shared
// base — a user-scope [sandbox.agent.host] must survive a project file that
// also defines one.
func TestLoad_UnionsEveryHostSectionAcrossScopes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userDir := filepath.Join(home, ".config", "agent-sandbox")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "config.toml"), []byte(`
[sandbox.host]
capabilities = ["go"]

[sandbox.agent.host]
capabilities = ["ssh"]

[sandbox.command.host]
allow_env = ["CI"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := writeTOML(t, baseProject+`
[sandbox.host]
capabilities = ["python"]

[sandbox.agent.host]
capabilities = ["docker"]

[sandbox.command.host]
allow_env = ["AWS_PROFILE"]
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, tc := range []struct {
		name string
		got  []string
		want []string
	}{
		{"Host.Capabilities", cfg.Sandbox.Host.Capabilities, []string{"go", "python"}},
		{"Agent.Host.Capabilities", cfg.Sandbox.Agent.Host.Capabilities, []string{"ssh", "docker"}},
		{"Command.Host.AllowEnv", cfg.Sandbox.Command.Host.AllowEnv, []string{"CI", "AWS_PROFILE"}},
	} {
		if !slices.Equal(tc.got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}
