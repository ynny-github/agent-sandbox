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

// baseProject is the minimum required config; host tests append the sandbox sections.
const baseProject = `
tool_mode = "mcp"
[mcp]
command_output_dir = "/tmp/out"
`

func TestLoad_DecodesSharedSection(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from any real user config
	p := writeTOML(t, baseProject+`
[sandbox.shared]
capabilities = ["go", "ssh"]
read = ["~/.orbstack"]
allow_env = ["FOO", "BAR*"]
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Sandbox.Shared.Capabilities; len(got) != 2 || got[0] != "go" || got[1] != "ssh" {
		t.Errorf("Capabilities = %v", got)
	}
	if got := cfg.Sandbox.Shared.Read; len(got) != 1 || got[0] != "~/.orbstack" {
		t.Errorf("Read = %v", got)
	}
	if got := cfg.Sandbox.Shared.AllowEnv; len(got) != 2 || got[0] != "FOO" || got[1] != "BAR*" {
		t.Errorf("AllowEnv = %v", got)
	}
}

func TestLoad_UnionsSharedListsAcrossScopes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userDir := filepath.Join(home, ".config", "agent-sandbox")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "config.toml"), []byte(`
[sandbox.shared]
capabilities = ["go"]
read = ["~/.a"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := writeTOML(t, baseProject+`
[sandbox.shared]
capabilities = ["python", "go"]
read = ["~/.b"]
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// user-first order, de-duped: go (user) then python (project).
	if got := cfg.Sandbox.Shared.Capabilities; len(got) != 2 || got[0] != "go" || got[1] != "python" {
		t.Errorf("Capabilities = %v, want [go python]", got)
	}
	if got := cfg.Sandbox.Shared.Read; len(got) != 2 || got[0] != "~/.a" || got[1] != "~/.b" {
		t.Errorf("Read = %v, want [~/.a ~/.b]", got)
	}
}

func TestLoad_DecodesPerSideSections(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from any real user config
	p := writeTOML(t, baseProject+`
[sandbox.shared]
capabilities = ["go"]

[sandbox.agent]
capabilities = ["ssh"]
read = ["~/notes"]

[sandbox.shell]
capabilities = ["node"]
allow_env = ["CI"]
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Each section stands on its own: Load never copies grants between them.
	if got := cfg.Sandbox.Shared.Capabilities; len(got) != 1 || got[0] != "go" {
		t.Errorf("Shared.Capabilities = %v, want [go]", got)
	}
	if got := cfg.Sandbox.Agent.Capabilities; len(got) != 1 || got[0] != "ssh" {
		t.Errorf("Agent.Capabilities = %v, want [ssh]", got)
	}
	if got := cfg.Sandbox.Agent.Read; len(got) != 1 || got[0] != "~/notes" {
		t.Errorf("Agent.Read = %v, want [~/notes]", got)
	}
	if got := cfg.Sandbox.Shell.Capabilities; len(got) != 1 || got[0] != "node" {
		t.Errorf("Shell.Capabilities = %v, want [node]", got)
	}
	if got := cfg.Sandbox.Shell.AllowEnv; len(got) != 1 || got[0] != "CI" {
		t.Errorf("Shell.AllowEnv = %v, want [CI]", got)
	}
}

// The user/project union covers all three sections, not just the shared base —
// a user-scope [sandbox.agent] must survive a project file that also defines one.
func TestLoad_UnionsEverySectionAcrossScopes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userDir := filepath.Join(home, ".config", "agent-sandbox")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "config.toml"), []byte(`
[sandbox.shared]
capabilities = ["go"]

[sandbox.agent]
capabilities = ["ssh"]

[sandbox.shell]
allow_env = ["CI"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := writeTOML(t, baseProject+`
[sandbox.shared]
capabilities = ["python"]

[sandbox.agent]
capabilities = ["docker"]

[sandbox.shell]
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
		{"Shared.Capabilities", cfg.Sandbox.Shared.Capabilities, []string{"go", "python"}},
		{"Agent.Capabilities", cfg.Sandbox.Agent.Capabilities, []string{"ssh", "docker"}},
		{"Shell.AllowEnv", cfg.Sandbox.Shell.AllowEnv, []string{"CI", "AWS_PROFILE"}},
	} {
		if !slices.Equal(tc.got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

// [sandbox.agent] carries two kinds of key in one table: the embedded host
// grants and the command-routing lists. allow (a host directory) and
// allow_commands (a command pattern) are the pair that would be ambiguous if
// routing had kept its original key name, so pin that they land in separate
// fields.
func TestLoad_AgentSectionSeparatesGrantsFromRouting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := writeTOML(t, baseProject+`
[sandbox.agent]
capabilities = ["ssh"]
allow = ["/srv/scratch"]
allow_commands = ["go *"]
drop_commands = [{ pattern = "git *", message = "use safe git" }]
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Sandbox.Agent.Allow; len(got) != 1 || got[0] != "/srv/scratch" {
		t.Errorf("Agent.Allow = %v, want [/srv/scratch] (host grant)", got)
	}
	if got := cfg.Sandbox.Agent.AllowCommands; len(got) != 1 || got[0] != "go *" {
		t.Errorf("Agent.AllowCommands = %v, want [go *] (routing)", got)
	}
	if got := cfg.Sandbox.Agent.Capabilities; len(got) != 1 || got[0] != "ssh" {
		t.Errorf("Agent.Capabilities = %v, want [ssh]", got)
	}
	want := DropRule{Pattern: "git *", Message: "use safe git"}
	if got := cfg.Sandbox.Agent.DropCommands; len(got) != 1 || got[0] != want {
		t.Errorf("Agent.DropCommands = %v, want [%v]", got, want)
	}
}
