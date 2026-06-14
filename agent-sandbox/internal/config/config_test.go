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
[server]
output_dir = "/tmp/out"

[sandbox]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
image = "mysandbox"
`

func TestLoad_ValidConfig(t *testing.T) {
	path := writeToml(t, validBase+`
[allow_patterns]
patterns = ["git *", "make *"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.OutputDir != "/tmp/out" {
		t.Errorf("OutputDir = %q, want /tmp/out", cfg.Server.OutputDir)
	}
	if cfg.Sandbox.BuildContext != "./docker/sandbox" {
		t.Errorf("BuildContext = %q, want ./docker/sandbox", cfg.Sandbox.BuildContext)
	}
	if cfg.Sandbox.Dockerfile != "Dockerfile" {
		t.Errorf("Dockerfile = %q, want Dockerfile", cfg.Sandbox.Dockerfile)
	}
	if cfg.Sandbox.Image != "mysandbox" {
		t.Errorf("Image = %q, want mysandbox", cfg.Sandbox.Image)
	}
	if len(cfg.AllowPatterns.Patterns) != 2 {
		t.Errorf("Patterns len = %d, want 2", len(cfg.AllowPatterns.Patterns))
	}
}

func TestLoad_MissingOutputDir(t *testing.T) {
	path := writeToml(t, `
[sandbox]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
image = "mysandbox"
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrMissingOutputDir) {
		t.Errorf("err = %v, want ErrMissingOutputDir", err)
	}
}

func TestLoad_MissingSandboxBuildContext(t *testing.T) {
	path := writeToml(t, `
[server]
output_dir = "/tmp/out"

[sandbox]
dockerfile = "Dockerfile"
image = "mysandbox"
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrMissingSandboxBuildContext) {
		t.Errorf("err = %v, want ErrMissingSandboxBuildContext", err)
	}
}

func TestLoad_MissingSandboxDockerfile(t *testing.T) {
	path := writeToml(t, `
[server]
output_dir = "/tmp/out"

[sandbox]
build_context = "./docker/sandbox"
image = "mysandbox"
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrMissingSandboxDockerfile) {
		t.Errorf("err = %v, want ErrMissingSandboxDockerfile", err)
	}
}

func TestLoad_MissingSandboxImage(t *testing.T) {
	path := writeToml(t, `
[server]
output_dir = "/tmp/out"

[sandbox]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrMissingSandboxImage) {
		t.Errorf("err = %v, want ErrMissingSandboxImage", err)
	}
}

func TestLoad_BlankRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    error
	}{
		{
			name: "output_dir whitespace only",
			content: `
[server]
output_dir = "   "

[sandbox]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
image = "mysandbox"
`,
			want: config.ErrMissingOutputDir,
		},
		{
			name: "build_context whitespace only",
			content: `
[server]
output_dir = "/tmp/out"

[sandbox]
build_context = "  "
dockerfile = "Dockerfile"
image = "mysandbox"
`,
			want: config.ErrMissingSandboxBuildContext,
		},
		{
			name: "dockerfile whitespace only",
			content: `
[server]
output_dir = "/tmp/out"

[sandbox]
build_context = "./docker/sandbox"
dockerfile = "	"
image = "mysandbox"
`,
			want: config.ErrMissingSandboxDockerfile,
		},
		{
			name: "image whitespace only",
			content: `
[server]
output_dir = "/tmp/out"

[sandbox]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
image = "  "
`,
			want: config.ErrMissingSandboxImage,
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

func TestLoad_EmptyAllowPatterns(t *testing.T) {
	path := writeToml(t, validBase+`
[allow_patterns]
patterns = []
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.AllowPatterns.Patterns) != 0 {
		t.Errorf("Patterns = %v, want empty slice", cfg.AllowPatterns.Patterns)
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

func TestLoad_AllowPatternsOmitted(t *testing.T) {
	path := writeToml(t, validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AllowPatterns.Patterns != nil && len(cfg.AllowPatterns.Patterns) != 0 {
		t.Errorf("Patterns = %v, want nil or empty", cfg.AllowPatterns.Patterns)
	}
}

func TestLoad_DenyPatterns_Loaded(t *testing.T) {
	path := writeToml(t, validBase+`
[deny_patterns]
patterns = ["go run *", "go generate *"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.DenyPatterns.Patterns) != 2 {
		t.Errorf("DenyPatterns len = %d, want 2", len(cfg.DenyPatterns.Patterns))
	}
	if cfg.DenyPatterns.Patterns[0] != "go run *" {
		t.Errorf("DenyPatterns[0] = %q, want \"go run *\"", cfg.DenyPatterns.Patterns[0])
	}
}

func TestLoad_DenyPatternsOmitted_IsEmpty(t *testing.T) {
	path := writeToml(t, validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.DenyPatterns.Patterns) != 0 {
		t.Errorf("DenyPatterns = %v, want empty", cfg.DenyPatterns.Patterns)
	}
}

func TestLoad_Container_Loaded(t *testing.T) {
	path := writeToml(t, validBase+`
[container]
env_passthrough = ["AWS_PROFILE", "HOME"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Container.EnvPassthrough) != 2 {
		t.Errorf("EnvPassthrough len = %d, want 2", len(cfg.Container.EnvPassthrough))
	}
	if cfg.Container.EnvPassthrough[0] != "AWS_PROFILE" {
		t.Errorf("EnvPassthrough[0] = %q, want AWS_PROFILE", cfg.Container.EnvPassthrough[0])
	}
}

func TestLoad_ContainerOmitted_IsEmpty(t *testing.T) {
	path := writeToml(t, validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Container.EnvPassthrough) != 0 {
		t.Errorf("EnvPassthrough = %v, want empty", cfg.Container.EnvPassthrough)
	}
}

func TestLoad_AllowCIDRs_Loaded(t *testing.T) {
	path := writeToml(t, `
[server]
output_dir = "/tmp/out"

[sandbox]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
image = "mysandbox"
allow_cidrs = ["192.168.0.0/16", "10.0.0.0/8"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Sandbox.AllowCIDRs) != 2 {
		t.Fatalf("AllowCIDRs len = %d, want 2", len(cfg.Sandbox.AllowCIDRs))
	}
	if cfg.Sandbox.AllowCIDRs[0] != "192.168.0.0/16" {
		t.Errorf("AllowCIDRs[0] = %q, want 192.168.0.0/16", cfg.Sandbox.AllowCIDRs[0])
	}
}

func TestLoad_AllowHosts_Loaded(t *testing.T) {
	path := writeToml(t, `
[server]
output_dir = "/tmp/out"

[sandbox]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
image = "mysandbox"
allow_hosts = ["api.github.com", "registry.npmjs.org"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Sandbox.AllowHosts) != 2 {
		t.Fatalf("AllowHosts len = %d, want 2", len(cfg.Sandbox.AllowHosts))
	}
	if cfg.Sandbox.AllowHosts[0] != "api.github.com" {
		t.Errorf("AllowHosts[0] = %q, want api.github.com", cfg.Sandbox.AllowHosts[0])
	}
}

func TestLoad_AllowCIDRs_OmittedIsEmpty(t *testing.T) {
	path := writeToml(t, validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Sandbox.AllowCIDRs) != 0 {
		t.Errorf("AllowCIDRs = %v, want empty", cfg.Sandbox.AllowCIDRs)
	}
}

func TestLoad_AllowHosts_OmittedIsEmpty(t *testing.T) {
	path := writeToml(t, validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Sandbox.AllowHosts) != 0 {
		t.Errorf("AllowHosts = %v, want empty", cfg.Sandbox.AllowHosts)
	}
}

func TestLoad_ExternalNetwork_Loaded(t *testing.T) {
	path := writeToml(t, `
[server]
output_dir = "/tmp/out"

[sandbox]
build_context = "./docker/sandbox"
dockerfile = "Dockerfile"
image = "mysandbox"
external_network = "backend"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Sandbox.ExternalNetwork != "backend" {
		t.Errorf("ExternalNetwork = %q, want \"backend\"", cfg.Sandbox.ExternalNetwork)
	}
}

func TestLoad_ExternalNetwork_OmittedIsEmpty(t *testing.T) {
	path := writeToml(t, validBase)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Sandbox.ExternalNetwork != "" {
		t.Errorf("ExternalNetwork = %q, want empty", cfg.Sandbox.ExternalNetwork)
	}
}

func TestLoad_MissingSandboxSection_ErrorsOnBuildContext(t *testing.T) {
	path := writeToml(t, `
[server]
output_dir = "/tmp/out"
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrMissingSandboxBuildContext) {
		t.Errorf("err = %v, want ErrMissingSandboxBuildContext", err)
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

func TestLoad_Nono_SubcommandWrap(t *testing.T) {
	path := writeToml(t, validBase+`
[nono]
profile = "nono.json"
subcommand = "wrap"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Nono.Subcommand != "wrap" {
		t.Errorf("Nono.Subcommand = %q, want \"wrap\"", cfg.Nono.Subcommand)
	}
}

func TestLoad_Nono_SubcommandRun(t *testing.T) {
	path := writeToml(t, validBase+`
[nono]
profile = "nono.json"
subcommand = "run"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Nono.Subcommand != "run" {
		t.Errorf("Nono.Subcommand = %q, want \"run\"", cfg.Nono.Subcommand)
	}
}

func TestLoad_Nono_SubcommandOmitted(t *testing.T) {
	path := writeToml(t, validBase+`
[nono]
profile = "nono.json"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Nono.Subcommand != "" {
		t.Errorf("Nono.Subcommand = %q, want \"\"", cfg.Nono.Subcommand)
	}
}

func TestLoad_Nono_SubcommandWhitespace(t *testing.T) {
	path := writeToml(t, validBase+`
[nono]
profile = "nono.json"
subcommand = " wrap "
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Nono.Subcommand != "wrap" {
		t.Errorf("Nono.Subcommand = %q, want \"wrap\" (trimmed)", cfg.Nono.Subcommand)
	}
}

func TestLoad_Nono_SubcommandInvalid(t *testing.T) {
	path := writeToml(t, validBase+`
[nono]
profile = "nono.json"
subcommand = "exec"
`)
	_, err := config.Load(path)
	if !errors.Is(err, config.ErrInvalidNonoSubcommand) {
		t.Errorf("err = %v, want ErrInvalidNonoSubcommand", err)
	}
}
