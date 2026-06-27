package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

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

[sandbox.container]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
image = "mysandbox"
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
	if cfg.Sandbox.Container.BuildContext != "./docker/sandbox" {
		t.Errorf("BuildContext = %q, want ./docker/sandbox", cfg.Sandbox.Container.BuildContext)
	}
	if cfg.Sandbox.Container.Dockerfile != "Dockerfile" {
		t.Errorf("Dockerfile = %q, want Dockerfile", cfg.Sandbox.Container.Dockerfile)
	}
	if cfg.Sandbox.Container.Image != "mysandbox" {
		t.Errorf("Image = %q, want mysandbox", cfg.Sandbox.Container.Image)
	}
	if len(cfg.Sandbox.Command.Allow) != 2 {
		t.Errorf("Allow len = %d, want 2", len(cfg.Sandbox.Command.Allow))
	}
}

func TestLoad_MissingMCPCommandOutputDir(t *testing.T) {
	path := writeToml(t, `
[sandbox.container]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
image = "mysandbox"
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrMissingMCPCommandOutputDir) {
		t.Errorf("err = %v, want ErrMissingMCPCommandOutputDir", err)
	}
}

func TestLoad_MissingContainerBuildContext(t *testing.T) {
	path := writeToml(t, `
[mcp]
command_output_dir = "/tmp/out"

[sandbox.container]
dockerfile = "Dockerfile"
image = "mysandbox"
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrMissingContainerBuildContext) {
		t.Errorf("err = %v, want ErrMissingContainerBuildContext", err)
	}
}

func TestLoad_MissingContainerDockerfile(t *testing.T) {
	path := writeToml(t, `
[mcp]
command_output_dir = "/tmp/out"

[sandbox.container]
build_context = "./docker/sandbox"
image = "mysandbox"
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrMissingContainerDockerfile) {
		t.Errorf("err = %v, want ErrMissingContainerDockerfile", err)
	}
}

func TestLoad_MissingContainerImage(t *testing.T) {
	path := writeToml(t, `
[mcp]
command_output_dir = "/tmp/out"

[sandbox.container]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrMissingContainerImage) {
		t.Errorf("err = %v, want ErrMissingContainerImage", err)
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

[sandbox.container]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
image = "mysandbox"
`,
			want: config.ErrMissingMCPCommandOutputDir,
		},
		{
			name: "build_context whitespace only",
			content: `
[mcp]
command_output_dir = "/tmp/out"

[sandbox.container]
build_context = "  "
dockerfile = "Dockerfile"
image = "mysandbox"
`,
			want: config.ErrMissingContainerBuildContext,
		},
		{
			name: "dockerfile whitespace only",
			content: `
[mcp]
command_output_dir = "/tmp/out"

[sandbox.container]
build_context = "./docker/sandbox"
dockerfile = "	"
image = "mysandbox"
`,
			want: config.ErrMissingContainerDockerfile,
		},
		{
			name: "image whitespace only",
			content: `
[mcp]
command_output_dir = "/tmp/out"

[sandbox.container]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
image = "  "
`,
			want: config.ErrMissingContainerImage,
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
drop = ["rm -rf *", "sudo *"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Sandbox.Command.Drop) != 2 {
		t.Errorf("Drop len = %d, want 2", len(cfg.Sandbox.Command.Drop))
	}
	if cfg.Sandbox.Command.Drop[0] != "rm -rf *" {
		t.Errorf("Drop[0] = %q, want \"rm -rf *\"", cfg.Sandbox.Command.Drop[0])
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

func TestLoad_Container_EnvPassthrough_Loaded(t *testing.T) {
	path := writeToml(t, validBase+`
env_passthrough = ["AWS_PROFILE", "HOME"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Sandbox.Container.EnvPassthrough) != 2 {
		t.Errorf("EnvPassthrough len = %d, want 2", len(cfg.Sandbox.Container.EnvPassthrough))
	}
	if cfg.Sandbox.Container.EnvPassthrough[0] != "AWS_PROFILE" {
		t.Errorf("EnvPassthrough[0] = %q, want AWS_PROFILE", cfg.Sandbox.Container.EnvPassthrough[0])
	}
}

func TestLoad_Container_EnvPassthroughOmitted_IsEmpty(t *testing.T) {
	path := writeToml(t, validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Sandbox.Container.EnvPassthrough) != 0 {
		t.Errorf("EnvPassthrough = %v, want empty", cfg.Sandbox.Container.EnvPassthrough)
	}
}

func TestLoad_AllowCIDRs_Loaded(t *testing.T) {
	path := writeToml(t, validBase+`
[sandbox.network]
allow_cidrs = ["192.168.0.0/16", "10.0.0.0/8"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Sandbox.Network.AllowCIDRs) != 2 {
		t.Fatalf("AllowCIDRs len = %d, want 2", len(cfg.Sandbox.Network.AllowCIDRs))
	}
	if cfg.Sandbox.Network.AllowCIDRs[0] != "192.168.0.0/16" {
		t.Errorf("AllowCIDRs[0] = %q, want 192.168.0.0/16", cfg.Sandbox.Network.AllowCIDRs[0])
	}
}

func TestLoad_AllowHosts_Loaded(t *testing.T) {
	path := writeToml(t, validBase+`
[sandbox.network]
allow_hosts = ["api.github.com", "registry.npmjs.org"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Sandbox.Network.AllowHosts) != 2 {
		t.Fatalf("AllowHosts len = %d, want 2", len(cfg.Sandbox.Network.AllowHosts))
	}
	if cfg.Sandbox.Network.AllowHosts[0] != "api.github.com" {
		t.Errorf("AllowHosts[0] = %q, want api.github.com", cfg.Sandbox.Network.AllowHosts[0])
	}
}

func TestLoad_AllowCIDRs_OmittedIsEmpty(t *testing.T) {
	path := writeToml(t, validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Sandbox.Network.AllowCIDRs) != 0 {
		t.Errorf("AllowCIDRs = %v, want empty", cfg.Sandbox.Network.AllowCIDRs)
	}
}

func TestLoad_AllowHosts_OmittedIsEmpty(t *testing.T) {
	path := writeToml(t, validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Sandbox.Network.AllowHosts) != 0 {
		t.Errorf("AllowHosts = %v, want empty", cfg.Sandbox.Network.AllowHosts)
	}
}

func TestLoad_ExternalNetwork_Loaded(t *testing.T) {
	path := writeToml(t, `
[mcp]
command_output_dir = "/tmp/out"

[sandbox.container]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
image = "mysandbox"
external_network = "backend"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Sandbox.Container.ExternalNetwork != "backend" {
		t.Errorf("ExternalNetwork = %q, want \"backend\"", cfg.Sandbox.Container.ExternalNetwork)
	}
}

func TestLoad_ExternalNetwork_OmittedIsEmpty(t *testing.T) {
	path := writeToml(t, validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Sandbox.Container.ExternalNetwork != "" {
		t.Errorf("ExternalNetwork = %q, want empty", cfg.Sandbox.Container.ExternalNetwork)
	}
}

func TestLoad_MissingSandboxSection_ErrorsOnBuildContext(t *testing.T) {
	path := writeToml(t, `
[mcp]
command_output_dir = "/tmp/out"
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrMissingContainerBuildContext) {
		t.Errorf("err = %v, want ErrMissingContainerBuildContext", err)
	}
}

func TestLoad_Nono_Loaded(t *testing.T) {
	path := writeToml(t, validBase+`
[nono]
profile = "nono.json"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Nono.Profile != "nono.json" {
		t.Errorf("Nono.Profile = %q, want nono.json", cfg.Nono.Profile)
	}
}

func TestLoad_Nono_OmittedIsEmpty(t *testing.T) {
	path := writeToml(t, validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Nono.Profile != "" {
		t.Errorf("Nono.Profile = %q, want empty", cfg.Nono.Profile)
	}
}

func TestLoad_Nono_LegacySubcommandIgnored(t *testing.T) {
	path := writeToml(t, validBase+`
[nono]
profile = "nono.json"
subcommand = "run"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Nono.Profile != "nono.json" {
		t.Errorf("Profile = %q, want \"nono.json\"", cfg.Nono.Profile)
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
