package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	return path
}

const validBase = `
[mcp]
command_output_dir = "/tmp/out"
`

func TestLoad_ValidConfig(t *testing.T) {
	path := writeToml(t, validBase+`
[sandbox.command]
allow = ["git *", "make *"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MCP.CommandOutputDir != "/tmp/out" {
		t.Errorf("CommandOutputDir = %q, want /tmp/out", cfg.MCP.CommandOutputDir)
	}
	if len(cfg.Sandbox.Command.Allow) != 2 {
		t.Errorf("Allow len = %d, want 2", len(cfg.Sandbox.Command.Allow))
	}
}

func TestLoad_MissingMCPCommandOutputDir(t *testing.T) {
	path := writeToml(t, `
[sandbox.command]
allow = ["git *"]
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrMissingMCPCommandOutputDir) {
		t.Errorf("err = %v, want ErrMissingMCPCommandOutputDir", err)
	}
}

func TestLoad_BlankRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    error
	}{
		{
			name: "command_output_dir whitespace only",
			content: `
[mcp]
command_output_dir = "   "
`,
			want: config.ErrMissingMCPCommandOutputDir,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeToml(t, tt.content)
			_, err := config.Load(path)
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
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
	if !errors.Is(err, config.ErrMissingMCPCommandOutputDir) {
		t.Errorf("err = %v, want ErrMissingMCPCommandOutputDir (old keys must not satisfy required fields)", err)
	}
}

func TestLoad_EmptyAllow(t *testing.T) {
	path := writeToml(t, validBase+`
[sandbox.command]
allow = []
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Sandbox.Command.Allow) != 0 {
		t.Errorf("Allow = %v, want empty slice", cfg.Sandbox.Command.Allow)
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

func TestLoad_AllowOmitted(t *testing.T) {
	path := writeToml(t, validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Sandbox.Command.Allow != nil && len(cfg.Sandbox.Command.Allow) != 0 {
		t.Errorf("Allow = %v, want nil or empty", cfg.Sandbox.Command.Allow)
	}
}

func TestLoad_Drop_Loaded(t *testing.T) {
	path := writeToml(t, validBase+`
[sandbox.command]
drop = [{ pattern = "rm -rf *" }, { pattern = "sudo *" }]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Sandbox.Command.Drop) != 2 {
		t.Errorf("Drop len = %d, want 2", len(cfg.Sandbox.Command.Drop))
	}
	if cfg.Sandbox.Command.Drop[0].Pattern != "rm -rf *" {
		t.Errorf("Drop[0].Pattern = %q, want \"rm -rf *\"", cfg.Sandbox.Command.Drop[0].Pattern)
	}
}

func TestLoad_DropRules_WithAndWithoutMessage(t *testing.T) {
	path := writeToml(t, validBase+`
[sandbox.command]
drop = [
  { pattern = "rm -rf *" },
  { pattern = "gh *", message = "gh is disabled" },
  { pattern = "sudo *" },
]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []config.DropRule{
		{Pattern: "rm -rf *"},
		{Pattern: "gh *", Message: "gh is disabled"},
		{Pattern: "sudo *"},
	}
	if !reflect.DeepEqual(cfg.Sandbox.Command.Drop, want) {
		t.Errorf("Drop = %+v, want %+v", cfg.Sandbox.Command.Drop, want)
	}
}

func TestLoad_DropRule_TableMissingPattern_Errors(t *testing.T) {
	path := writeToml(t, validBase+`
[sandbox.command]
drop = [{ message = "no pattern here" }]
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected an error for a drop table without a pattern, got nil")
	}
}

func TestLoad_DropOmitted_IsEmpty(t *testing.T) {
	path := writeToml(t, validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Sandbox.Command.Drop) != 0 {
		t.Errorf("Drop = %v, want empty", cfg.Sandbox.Command.Drop)
	}
}

func TestLoad_DeprecatedAllowCIDRs_Rejected(t *testing.T) {
	path := writeToml(t, validBase+`
[sandbox.network]
allow_cidrs = ["10.0.0.0/8"]
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrDeprecatedNetworkKeys) {
		t.Errorf("err = %v, want ErrDeprecatedNetworkKeys", err)
	}
}

func TestLoad_DeprecatedAllowHosts_Rejected(t *testing.T) {
	path := writeToml(t, validBase+`
[sandbox.network]
allow_hosts = ["api.github.com"]
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrDeprecatedNetworkKeys) {
		t.Errorf("err = %v, want ErrDeprecatedNetworkKeys", err)
	}
}

func TestLoad_ToolMode_DefaultsToMcp(t *testing.T) {
	path := writeToml(t, validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ToolMode != "mcp" {
		t.Errorf("ToolMode = %q, want \"mcp\" (default)", cfg.ToolMode)
	}
}

func TestLoad_ToolMode_HookAccepted(t *testing.T) {
	path := writeToml(t, "tool_mode = \"hook\"\n"+validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ToolMode != "hook" {
		t.Errorf("ToolMode = %q, want \"hook\"", cfg.ToolMode)
	}
}

func TestLoad_ToolMode_InvalidRejected(t *testing.T) {
	path := writeToml(t, "tool_mode = \"bogus\"\n"+validBase)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrInvalidToolMode) {
		t.Errorf("err = %v, want ErrInvalidToolMode", err)
	}
}

func TestLoad_HookModeAllowsMissingCommandOutputDir(t *testing.T) {
	path := writeToml(t, `
tool_mode = "hook"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.MCP.CommandOutputDir != "" {
		t.Errorf("CommandOutputDir = %q, want empty", cfg.MCP.CommandOutputDir)
	}
}

func TestLoad_HookModeIgnoresCommandOutputDir(t *testing.T) {
	path := writeToml(t, "tool_mode = \"hook\"\n"+validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.MCP.CommandOutputDir != "/tmp/out" {
		t.Errorf("CommandOutputDir = %q, want /tmp/out (kept but unused)", cfg.MCP.CommandOutputDir)
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

func TestLoad_Compose_ScalarProjectWins(t *testing.T) {
	writeUserToml(t, `
tool_mode = "mcp"
[mcp]
command_output_dir = "/u/out"
`)
	project := writeToml(t, `
tool_mode = "hook"
`)
	cfg, err := config.Load(project)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ToolMode != "hook" {
		t.Errorf("ToolMode = %q, want \"hook\" (project wins)", cfg.ToolMode)
	}
	if cfg.MCP.CommandOutputDir != "/u/out" {
		t.Errorf("CommandOutputDir = %q, want \"/u/out\" (user retained, project omitted it)", cfg.MCP.CommandOutputDir)
	}
}

func TestLoad_Compose_ListUnion(t *testing.T) {
	// The user lists are >= the project lists in length on purpose: TOML decode
	// reuses the user snapshot's backing array in place when its cap suffices, so
	// these cases only pass if Load clones the snapshot before the project decode.
	writeUserToml(t, `
[mcp]
command_output_dir = "/u/out"
[sandbox.command]
allow = ["git *", "make *"]
drop = [{ pattern = "rm *" }]
env_passthrough = ["HOME", "AWS_PROFILE"]
`)
	project := writeToml(t, `
[sandbox.command]
allow = ["make *", "npm *"]
drop = [{ pattern = "sudo *" }]
env_passthrough = ["CI"]
`)
	cfg, err := config.Load(project)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.Sandbox.Command.Allow; !slices.Equal(got, []string{"git *", "make *", "npm *"}) {
		t.Errorf("Allow = %v, want [git * make * npm *]", got)
	}
	gotDrop := make([]string, len(cfg.Sandbox.Command.Drop))
	for i, r := range cfg.Sandbox.Command.Drop {
		gotDrop[i] = r.Pattern
	}
	if !slices.Equal(gotDrop, []string{"rm *", "sudo *"}) {
		t.Errorf("Drop = %v, want [rm * sudo *]", gotDrop)
	}
	if got := cfg.Sandbox.Command.EnvPassthrough; !slices.Equal(got, []string{"HOME", "AWS_PROFILE", "CI"}) {
		t.Errorf("EnvPassthrough = %v, want [HOME AWS_PROFILE CI]", got)
	}
}

func TestLoad_Compose_NoHome_ProjectOnly(t *testing.T) {
	t.Setenv("HOME", "")
	project := writeToml(t, validBase)
	cfg, err := config.Load(project)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MCP.CommandOutputDir != "/tmp/out" {
		t.Errorf("CommandOutputDir = %q, want /tmp/out (project-only)", cfg.MCP.CommandOutputDir)
	}
}

func TestLoad_Compose_DeprecatedKeyInUserFile(t *testing.T) {
	writeUserToml(t, `
[sandbox.network]
allow_cidrs = ["10.0.0.0/8"]
`)
	project := writeToml(t, validBase)
	if _, err := config.Load(project); !errors.Is(err, config.ErrDeprecatedNetworkKeys) {
		t.Errorf("err = %v, want ErrDeprecatedNetworkKeys", err)
	}
}

func TestLoad_Compose_ValidationOnMerged_UserSuppliesRequired(t *testing.T) {
	writeUserToml(t, `
[mcp]
command_output_dir = "/u/out"
`)
	// Project omits the required field; validate runs on the merged config, so
	// the user's value must satisfy it.
	project := writeToml(t, `
tool_mode = "mcp"
`)
	if _, err := config.Load(project); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_Compose_ValidationOnMerged_MissingEverywhere(t *testing.T) {
	writeUserToml(t, `
tool_mode = "mcp"
`)
	// command_output_dir is absent from both scopes -> merged config fails validation.
	project := writeToml(t, `
tool_mode = "mcp"
`)
	if _, err := config.Load(project); !errors.Is(err, config.ErrMissingMCPCommandOutputDir) {
		t.Errorf("err = %v, want ErrMissingMCPCommandOutputDir", err)
	}
}

func TestLoad_NetworkAllowDomains(t *testing.T) {
	path := writeToml(t, `
tool_mode = "hook"

[sandbox.network]
allow_domains = ["proxy.golang.org", "sum.golang.org"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{"proxy.golang.org", "sum.golang.org"}
	if !slices.Equal(cfg.Sandbox.Network.AllowDomains, want) {
		t.Errorf("AllowDomains = %v, want %v", cfg.Sandbox.Network.AllowDomains, want)
	}
}

func TestLoad_CommandEnvPassthrough(t *testing.T) {
	path := writeToml(t, `
tool_mode = "hook"

[sandbox.command]
env_passthrough = ["AWS_PROFILE", "MISE_SHELL"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{"AWS_PROFILE", "MISE_SHELL"}
	if !slices.Equal(cfg.Sandbox.Command.EnvPassthrough, want) {
		t.Errorf("EnvPassthrough = %v, want %v", cfg.Sandbox.Command.EnvPassthrough, want)
	}
}

func TestLoad_RejectsNonoEnvPassthrough(t *testing.T) {
	path := writeToml(t, `
tool_mode = "hook"

[sandbox.command]
env_passthrough = ["AWS_PROFILE", "NONO_ALLOW_DOMAIN"]
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrEnvPassthroughNonoVar) {
		t.Fatalf("Load() error = %v, want ErrEnvPassthroughNonoVar", err)
	}
}

func TestLoad_RejectsRemovedContainerSection(t *testing.T) {
	path := writeToml(t, `
tool_mode = "hook"

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
tool_mode = "hook"

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
	project := writeToml(t, validBase)
	if _, err := config.Load(project); !errors.Is(err, config.ErrRemovedContainerSection) {
		t.Errorf("err = %v, want ErrRemovedContainerSection", err)
	}
}
