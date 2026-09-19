package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

// TestMain isolates HOME to an empty temp dir so tests never pick up a real
// ~/.config/agent-sandbox/config.toml. Tests that need a user config override
// HOME via t.Setenv.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "agent-sandbox-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func writeToml(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	// Every fixture needs a command profile to satisfy validate's
	// ErrCommandProfileMissing check; writing the default name beside the
	// config keeps these fixtures exercising the default-path resolution
	// instead of pointing command_profile somewhere else.
	writeFile(t, filepath.Join(filepath.Dir(path), "command-profile.json"), "{}")
	return path
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestCommandProfilePathDefaultsBesideConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "")
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "command-profile.json")
	if got := cfg.CommandProfilePath(); got != want {
		t.Errorf("CommandProfilePath() = %q, want %q", got, want)
	}
}

func TestCommandProfilePathHonoursRelativeOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "command_profile = \"profiles/cmd.json\"\n")
	writeFile(t, filepath.Join(dir, "profiles", "cmd.json"), "{}")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "profiles", "cmd.json")
	if got := cfg.CommandProfilePath(); got != want {
		t.Errorf("CommandProfilePath() = %q, want %q", got, want)
	}
}

func TestCommandProfilePathKeepsAbsoluteOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	abs := filepath.Join(t.TempDir(), "elsewhere.json")
	writeFile(t, cfgPath, "command_profile = "+strconv.Quote(abs)+"\n")
	writeFile(t, abs, "{}")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.CommandProfilePath(); got != abs {
		t.Errorf("CommandProfilePath() = %q, want %q", got, abs)
	}
}

func TestValidateRejectsMissingCommandProfile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "")

	cfg, err := config.Load(cfgPath)
	if !errors.Is(err, config.ErrCommandProfileMissing) {
		t.Fatalf("Load error = %v, want ErrCommandProfileMissing", err)
	}
	// Unlike every other validate failure, this one still returns cfg: doctor's
	// checkProfiles (cmd/doctor.go) needs cfg.CommandProfilePath() to
	// report its own dedicated, actionable hint instead of the generic
	// "fix the config first" one.
	if cfg == nil {
		t.Fatal("Load(cfg) = nil, want the partially-validated config alongside ErrCommandProfileMissing")
	}
}

// An empty config is valid: every key has a default, and the only thing
// validate still insists on — the command profile — is written beside it by
// writeToml under its default name.
func TestLoad_EmptyConfigIsValid(t *testing.T) {
	path := writeToml(t, "")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(filepath.Dir(path), "command-profile.json")
	if got := cfg.CommandProfilePath(); got != want {
		t.Errorf("CommandProfilePath() = %q, want %q", got, want)
	}
}

func TestLoad_OldKeysRejected(t *testing.T) {
	path := writeToml(t, `
[server]
output_dir = "/tmp/out"

[sandbox]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
image = "mysandbox"
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrMovedAgentSectionToProfile) {
		t.Errorf("err = %v, want ErrMovedAgentSectionToProfile (old keys must be rejected by name, not silently ignored)", err)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := config.Load("/nonexistent/path/config.toml")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want to wrap os.ErrNotExist", err)
	}
}

func TestLoad_DeprecatedAllowCIDRs_Rejected(t *testing.T) {
	path := writeToml(t, `
[sandbox.network]
allow_cidrs = ["10.0.0.0/8"]
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrDeprecatedNetworkKeys) {
		t.Errorf("err = %v, want ErrDeprecatedNetworkKeys", err)
	}
}

func TestLoad_DeprecatedAllowHosts_Rejected(t *testing.T) {
	path := writeToml(t, `
[sandbox.network]
allow_hosts = ["api.github.com"]
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrDeprecatedNetworkKeys) {
		t.Errorf("err = %v, want ErrDeprecatedNetworkKeys", err)
	}
}

// writeUserToml points HOME at an isolated temp dir and writes a user-scope
// config there, so config.Load discovers it at ~/.config/agent-sandbox/config.toml.
func writeUserToml(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "agent-sandbox")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// command_profile is the only scalar left, so it is what pins the compose
// rule: the project file wins for a key both declare.
func TestLoad_Compose_ScalarProjectWins(t *testing.T) {
	writeUserToml(t, "command_profile = \"user-cmd.json\"\n")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "command_profile = \"project-cmd.json\"\n")
	writeFile(t, filepath.Join(dir, "project-cmd.json"), "{}")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := cfg.CommandProfilePath(), filepath.Join(dir, "project-cmd.json"); got != want {
		t.Errorf("CommandProfilePath() = %q, want %q (project wins)", got, want)
	}
}

// The mirror of the above: a key only the user file declares survives into the
// merged config, and validate runs on that merged result.
func TestLoad_Compose_UserScalarRetained(t *testing.T) {
	writeUserToml(t, "command_profile = \"user-cmd.json\"\n")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "")
	// Deliberately not the default name: only the user-scope value can make
	// this config validate.
	writeFile(t, filepath.Join(dir, "user-cmd.json"), "{}")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := cfg.CommandProfilePath(), filepath.Join(dir, "user-cmd.json"); got != want {
		t.Errorf("CommandProfilePath() = %q, want %q (user value retained)", got, want)
	}
}

func TestLoad_Compose_NoHome_ProjectOnly(t *testing.T) {
	t.Setenv("HOME", "")
	project := writeToml(t, "command_profile = \"command-profile.json\"\n")
	cfg, err := config.Load(project)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(filepath.Dir(project), "command-profile.json")
	if got := cfg.CommandProfilePath(); got != want {
		t.Errorf("CommandProfilePath() = %q, want %q (project-only)", got, want)
	}
}

func TestLoad_Compose_DeprecatedKeyInUserFile(t *testing.T) {
	writeUserToml(t, `
[sandbox.network]
allow_cidrs = ["10.0.0.0/8"]
`)
	project := writeToml(t, "")
	if _, err := config.Load(project); !errors.Is(err, config.ErrDeprecatedNetworkKeys) {
		t.Errorf("err = %v, want ErrDeprecatedNetworkKeys", err)
	}
}

// Every key that moved as the sandbox sections were reorganized. Loading an old
// spelling must say where it went rather than silently ignoring it — a config
// that is quietly half-applied is the failure mode worth spending errors on.
func TestLoad_RejectsMovedKeys(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    error
	}{
		{
			name: "sandbox.network section",
			content: `

[sandbox.network]
allow_domains = ["proxy.golang.org"]
`,
			want: config.ErrMovedNetworkSection,
		},
		{
			name: "command env_passthrough",
			content: `

[sandbox.command]
env_passthrough = ["CI"]
`,
			want: config.ErrMovedEnvPassthrough,
		},
		{
			name: "shared base under its old name",
			content: `

[sandbox.host]
capabilities = ["go"]
`,
			want: config.ErrMovedSharedToAgent,
		},
		{
			name: "agent host sub-table",
			content: `

[sandbox.agent.host]
capabilities = ["ssh"]
`,
			want: config.ErrMovedAgentHost,
		},
		{
			name: "command host sub-table",
			content: `

[sandbox.command.host]
capabilities = ["go"]
`,
			want: config.ErrMovedCommandHost,
		},
		{
			name: "command network sub-table",
			content: `

[sandbox.command.network]
allow_domains = ["proxy.golang.org"]
`,
			want: config.ErrMovedCommandNetwork,
		},
		{
			name: "command routing",
			content: `

[sandbox.command]
allow = ["go *"]
`,
			want: config.ErrMovedCommandTiers,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeToml(t, tt.content)
			if _, err := config.Load(path); !errors.Is(err, tt.want) {
				t.Errorf("Load() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestLoad_RejectsRemovedContainerSection(t *testing.T) {
	path := writeToml(t, `

[sandbox.container]
image = "sandbox:0.1.0"
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrRemovedContainerSection) {
		t.Fatalf("Load() error = %v, want ErrRemovedContainerSection", err)
	}
}

func TestLoad_RejectsRemovedAllowExternal(t *testing.T) {
	path := writeToml(t, `

[sandbox.network]
allow_external = true
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrRemovedAllowExternal) {
		t.Fatalf("Load() error = %v, want ErrRemovedAllowExternal", err)
	}
}

// TestLoad_RejectsRemovedContainerSection_UserScope pins the user-scope call
// site of checkDeprecated directly (Load's step 1, before the project decode)
// rather than only exercising it indirectly via the deprecated-allow_cidrs
// coverage in TestLoad_Compose_DeprecatedKeyInUserFile.
func TestLoad_RejectsRemovedContainerSection_UserScope(t *testing.T) {
	writeUserToml(t, `
[sandbox.container]
image = "sandbox:0.1.0"
`)
	project := writeToml(t, "")
	if _, err := config.Load(project); !errors.Is(err, config.ErrRemovedContainerSection) {
		t.Errorf("err = %v, want ErrRemovedContainerSection", err)
	}
}

func TestLoadRejectsAllowCommands(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "[sandbox.agent]\nallow_commands = [\"go *\"]\n")

	_, err := config.Load(cfgPath)
	if !errors.Is(err, config.ErrMovedCommandTiers) {
		t.Fatalf("Load error = %v, want ErrMovedCommandTiers", err)
	}
}

func TestLoadRejectsDropCommands(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "[sandbox.agent]\ndrop_commands = [{ pattern = \"git *\" }]\n")

	_, err := config.Load(cfgPath)
	if !errors.Is(err, config.ErrMovedCommandTiers) {
		t.Fatalf("Load error = %v, want ErrMovedCommandTiers", err)
	}
}

func TestLoadRejectsSharedSection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "[sandbox.shared]\ncapabilities = [\"go\"]\n")

	_, err := config.Load(cfgPath)
	if !errors.Is(err, config.ErrMovedSharedToAgent) {
		t.Fatalf("Load error = %v, want ErrMovedSharedToAgent", err)
	}
}

func TestLoadRejectsShellSection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "[sandbox.shell]\nallow_domains = [\"example.com\"]\n")

	_, err := config.Load(cfgPath)
	if !errors.Is(err, config.ErrMovedShellToProfile) {
		t.Fatalf("Load error = %v, want ErrMovedShellToProfile", err)
	}
}

func TestAgentProfilePathDefaultsBesideConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "")
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "claude-profile.json")
	if got := cfg.AgentProfilePath("claude"); got != want {
		t.Errorf("AgentProfilePath = %q, want %q", got, want)
	}
}

func TestAgentProfilePathRelativeResolvesAgainstConfigDir(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "\n[agents.claude]\nprofile = \"profiles/claude.json\"\n")
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "profiles", "claude.json")
	if got := cfg.AgentProfilePath("claude"); got != want {
		t.Errorf("AgentProfilePath = %q, want %q", got, want)
	}
}

func TestAgentProfilePathAbsoluteIsUsedAsIs(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "\n[agents.claude]\nprofile = \"/etc/agent-sandbox/claude.json\"\n")
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.AgentProfilePath("claude"); got != "/etc/agent-sandbox/claude.json" {
		t.Errorf("AgentProfilePath = %q, want the absolute path unchanged", got)
	}
}

// The project file wins for an agent both files declare, and an agent only the
// user-scope file declares survives. Both fall out of how BurntSushi/toml
// decodes into an existing map — pinned here because a second field on
// AgentConfig would silently break the first half (a key is replaced whole,
// not merged field by field).
func TestAgentProfileProjectOverridesUserScopeAndKeepsOtherAgents(t *testing.T) {
	writeUserToml(t, "[agents.claude]\nprofile = \"user-claude.json\"\n\n[agents.codex]\nprofile = \"user-codex.json\"\n")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	writeFile(t, cfgPath, "\n[agents.claude]\nprofile = \"project-claude.json\"\n")
	writeFile(t, filepath.Join(dir, "command-profile.json"), "{}")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.AgentProfilePath("claude"), filepath.Join(dir, "project-claude.json"); got != want {
		t.Errorf("project must win for an agent both files declare: got %q, want %q", got, want)
	}
	if got, want := cfg.AgentProfilePath("codex"), filepath.Join(dir, "user-codex.json"); got != want {
		t.Errorf("an agent only the user config declares must survive: got %q, want %q", got, want)
	}
}

// [sandbox] and everything under it are gone. A config that still carries one
// must fail loudly, naming where the grants moved — a half-ignored section is
// a sandbox that silently grants less than its author believes.
func TestLoadRejectsTheSandboxSection(t *testing.T) {
	for _, body := range []string{
		"[sandbox.agent]\ncapabilities = [\"go\"]\n",
		"[sandbox.agent]\nallow = [\"/opt\"]\n",
		"[sandbox.agent]\nallow_env = [\"FOO\"]\n",
		"[sandbox]\n",
	} {
		t.Run(body, func(t *testing.T) {
			_, err := config.Load(writeToml(t, body))
			if err == nil {
				t.Fatal("expected an error for a config still declaring [sandbox], got nil")
			}
			if !strings.Contains(err.Error(), "[agents.") {
				t.Errorf("the error must name where the grants moved: %v", err)
			}
		})
	}
}

// tool_mode and [mcp] are gone: the PreToolUse hook is the only way commands
// reach execd. Both must fail by name rather than be ignored — a config
// still saying tool_mode = "mcp" would otherwise launch a hook-routed session
// while its author believes Bash is disabled and every command goes through an
// MCP tool.
func TestLoad_RejectsRemovedToolMode(t *testing.T) {
	for _, value := range []string{`"mcp"`, `"hook"`, `"bogus"`} {
		t.Run(value, func(t *testing.T) {
			path := writeToml(t, "tool_mode = "+value+"\n")
			if _, err := config.Load(path); !errors.Is(err, config.ErrRemovedToolMode) {
				t.Errorf("Load() error = %v, want ErrRemovedToolMode", err)
			}
		})
	}
}

func TestLoad_RejectsRemovedMCPSection(t *testing.T) {
	path := writeToml(t, "[mcp]\ncommand_output_dir = \"/tmp/out\"\n")
	if _, err := config.Load(path); !errors.Is(err, config.ErrRemovedMCPSection) {
		t.Errorf("Load() error = %v, want ErrRemovedMCPSection", err)
	}
}

// A bare [mcp] with no key under it must fail too: checkDeprecated works off
// the decode metadata precisely so a key with no value still fires.
func TestLoad_RejectsRemovedMCPSection_Bare(t *testing.T) {
	path := writeToml(t, "[mcp]\n")
	if _, err := config.Load(path); !errors.Is(err, config.ErrRemovedMCPSection) {
		t.Errorf("Load() error = %v, want ErrRemovedMCPSection", err)
	}
}

// Both removals must fire from the user-scope file as well, not only the
// project one — Load checks each file as it decodes it.
func TestLoad_RejectsRemovedKeys_UserScope(t *testing.T) {
	writeUserToml(t, "tool_mode = \"hook\"\n")
	project := writeToml(t, "")
	if _, err := config.Load(project); !errors.Is(err, config.ErrRemovedToolMode) {
		t.Errorf("Load() error = %v, want ErrRemovedToolMode", err)
	}
}
