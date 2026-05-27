package executor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/executor"
)

func TestNewSandboxProject_Name(t *testing.T) {
	proj, err := executor.NewSandboxProject(12345, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := executor.ProjectSandboxName(cwd)
	if proj.Name != want {
		t.Errorf("Name = %q, want %q", proj.Name, want)
	}
}

func TestNewSandboxProject_ImageName(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := proj.Services[executor.SandboxServiceName]
	want := "agent-sandbox/myapp"
	if svc.Image != want {
		t.Errorf("Image = %q, want %q", svc.Image, want)
	}
}

func TestNewSandboxProject_BuildConfig(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "MyDockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := proj.Services[executor.SandboxServiceName]
	if svc.Build == nil {
		t.Fatal("Build is nil")
	}
	absCtx, _ := filepath.Abs("./ctx")
	if svc.Build.Context != absCtx {
		t.Errorf("Build.Context = %q, want %q", svc.Build.Context, absCtx)
	}
	if svc.Build.Dockerfile != "MyDockerfile" {
		t.Errorf("Dockerfile = %q, want MyDockerfile", svc.Build.Dockerfile)
	}
}

func TestNewSandboxProject_Labels(t *testing.T) {
	proj, err := executor.NewSandboxProject(99, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, serviceName := range []string{executor.SandboxServiceName, "gost"} {
		svc := proj.Services[serviceName]
		if svc.Labels["cr.managed"] != "true" {
			t.Errorf("%s label cr.managed = %q, want \"true\"", serviceName, svc.Labels["cr.managed"])
		}
		if svc.Labels["cr.project_dir"] != cwd {
			t.Errorf("%s label cr.project_dir = %q, want %q", serviceName, svc.Labels["cr.project_dir"], cwd)
		}
	}
}

func TestNewSandboxProject_VolumeMountsCwd(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := proj.Services[executor.SandboxServiceName]
	if len(svc.Volumes) != 1 {
		t.Fatalf("Volumes len = %d, want 1", len(svc.Volumes))
	}
	v := svc.Volumes[0]
	if v.Type != "bind" {
		t.Errorf("Type = %q, want bind", v.Type)
	}
	cwd, _ := os.Getwd()
	if v.Source != cwd {
		t.Errorf("Source = %q, want %q (cwd)", v.Source, cwd)
	}
	if v.Target != "/workspace" {
		t.Errorf("Target = %q, want /workspace", v.Target)
	}
}

func TestNewSandboxProject_ServiceName(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := proj.Services[executor.SandboxServiceName]; !ok {
		t.Errorf("service %q not found in project", executor.SandboxServiceName)
	}
}

func TestNewSandboxProject_WorkingDir(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cwd, _ := os.Getwd()
	if proj.WorkingDir != cwd {
		t.Errorf("WorkingDir = %q, want %q", proj.WorkingDir, cwd)
	}
}

func TestSandboxServiceName(t *testing.T) {
	if executor.SandboxServiceName != "workspace" {
		t.Errorf("SandboxServiceName = %q, want \"workspace\"", executor.SandboxServiceName)
	}
}

func TestProjectSandboxName_StableForSameCWD(t *testing.T) {
	cwd := filepath.Join("tmp", "my project")
	got1 := executor.ProjectSandboxName(cwd)
	got2 := executor.ProjectSandboxName(cwd)
	if got1 != got2 {
		t.Fatalf("ProjectSandboxName not stable: %q != %q", got1, got2)
	}
	if !strings.HasPrefix(got1, "cr-sandbox-my-project-") {
		t.Fatalf("ProjectSandboxName = %q, want cr-sandbox-my-project-*", got1)
	}
}

func TestProjectSandboxName_DifferentPathsWithSameBaseDiffer(t *testing.T) {
	got1 := executor.ProjectSandboxName(filepath.Join("tmp", "one", "app"))
	got2 := executor.ProjectSandboxName(filepath.Join("tmp", "two", "app"))
	if got1 == got2 {
		t.Fatalf("ProjectSandboxName collision for different paths: %q", got1)
	}
}

func TestProjectSandboxName_NormalizesUnsupportedCharacters(t *testing.T) {
	got := executor.ProjectSandboxName(filepath.Join("tmp", "My App!!!"))
	if !strings.HasPrefix(got, "cr-sandbox-my-app-") {
		t.Fatalf("ProjectSandboxName = %q, want normalized basename", got)
	}
	for _, r := range got {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			t.Fatalf("ProjectSandboxName contains unsupported rune %q in %q", r, got)
		}
	}
}

func TestNewSandboxProject_ProjectNameUsesCWD(t *testing.T) {
	pid := os.Getpid()
	proj, err := executor.NewSandboxProject(pid, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := executor.ProjectSandboxName(cwd)
	if proj.Name != want {
		t.Errorf("Name = %q, want %q", proj.Name, want)
	}
}

func TestNewSandboxProject_User(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := proj.Services[executor.SandboxServiceName]
	if svc.User != "1000:2000" {
		t.Errorf("User = %q, want \"1000:2000\"", svc.User)
	}
}

func TestNewSandboxProject_RootUserReturnsError(t *testing.T) {
	_, err := executor.NewSandboxProject(1, 0, 0, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err == nil {
		t.Fatal("expected error when uid=0 (root), got nil")
	}
	if !strings.Contains(err.Error(), "root") {
		t.Errorf("error should mention root: %v", err)
	}
}

func TestNewSandboxProject_RootGIDOnlyNotBlocked(t *testing.T) {
	// only uid=0 is blocked, gid=0 alone is allowed
	proj, err := executor.NewSandboxProject(1, 1000, 0, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("gid=0 with non-root uid should be allowed: %v", err)
	}
	svc := proj.Services[executor.SandboxServiceName]
	if svc.User != "1000:0" {
		t.Errorf("User = %q, want \"1000:0\"", svc.User)
	}
}

func TestNewSandboxProject_WorkingDir_Container(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := proj.Services[executor.SandboxServiceName]
	if svc.WorkingDir != "/workspace" {
		t.Errorf("svc.WorkingDir = %q, want \"/workspace\"", svc.WorkingDir)
	}
}

func TestNewSandboxProject_HasDefaultNetwork(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := proj.Networks["default"]; !ok {
		t.Error("project Networks does not contain \"default\"")
	}
	svc := proj.Services[executor.SandboxServiceName]
	if _, ok := svc.Networks["default"]; !ok {
		t.Error("service Networks does not contain \"default\"")
	}
}

func TestNewSandboxProject_HasGostService(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc, ok := proj.Services["gost"]
	if !ok {
		t.Fatal("gost service not found in project")
	}
	if svc.Image != "gogost/gost:3" {
		t.Errorf("gost image = %q, want gogost/gost:3", svc.Image)
	}
	if len(svc.Configs) != 1 {
		t.Fatalf("gost configs len = %d, want 1", len(svc.Configs))
	}
	if svc.Configs[0].Target != "/etc/gost/config.yaml" {
		t.Errorf("gost config target = %q, want /etc/gost/config.yaml", svc.Configs[0].Target)
	}
}

func TestNewSandboxProject_HasGostConfig(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg, ok := proj.Configs["gost_config"]
	if !ok {
		t.Fatal("gost_config not found in project.Configs")
	}
	if cfg.Content == "" {
		t.Error("gost_config Content should not be empty")
	}
}

// During Up, workspace uses default network (full internet for build/pull).
// After Up, ApplyNetworkPolicy moves it to sandbox_internal.
func TestNewSandboxProject_WorkspaceStartsOnDefaultNetwork(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := proj.Services[executor.SandboxServiceName]
	if _, ok := svc.Networks["default"]; !ok {
		t.Error("workspace should start on default network (for build/pull during Up)")
	}
}

func TestNewSandboxProject_GostOnBothNetworks(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gost := proj.Services["gost"]
	if _, ok := gost.Networks["sandbox_internal"]; !ok {
		t.Error("gost should be on sandbox_internal")
	}
	if _, ok := gost.Networks["default"]; !ok {
		t.Error("gost should be on default (to reach internet)")
	}
}

func TestNewSandboxProject_SandboxInternalNetworkIsInternal(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	net, ok := proj.Networks["sandbox_internal"]
	if !ok {
		t.Fatal("sandbox_internal network not found in project.Networks")
	}
	if !net.Internal {
		t.Error("sandbox_internal should have Internal=true")
	}
}

func TestNewSandboxProject_WorkspaceHasProxyEnv(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := proj.Services[executor.SandboxServiceName]
	checkEnv := func(key, want string) {
		t.Helper()
		v, ok := svc.Environment[key]
		if !ok || v == nil {
			t.Errorf("env %s missing", key)
			return
		}
		if *v != want {
			t.Errorf("env %s = %q, want %q", key, *v, want)
		}
	}
	checkEnv("HOME", "/tmp")
	checkEnv("HTTP_PROXY", "http://gost:3128")
	checkEnv("HTTPS_PROXY", "http://gost:3128")
	checkEnv("http_proxy", "http://gost:3128")
	checkEnv("https_proxy", "http://gost:3128")
	checkEnv("ALL_PROXY", "socks5://gost:1080")
	checkEnv("all_proxy", "socks5://gost:1080")
	checkEnv("NO_PROXY", "localhost,127.0.0.1,gost")
	checkEnv("no_proxy", "localhost,127.0.0.1,gost")
}

func TestGenerateGostConfig_HasSOCKS5(t *testing.T) {
	cfg := executor.GenerateGostConfig(nil, nil)
	if !strings.Contains(cfg, "type: socks5") {
		t.Error("config should contain socks5 service")
	}
	if !strings.Contains(cfg, `":1080"`) {
		t.Error("socks5 should listen on :1080")
	}
}

func TestGenerateGostConfig_HasHTTP(t *testing.T) {
	cfg := executor.GenerateGostConfig(nil, nil)
	if !strings.Contains(cfg, "type: http") {
		t.Error("config should contain http proxy service")
	}
	if !strings.Contains(cfg, `":3128"`) {
		t.Error("http proxy should listen on :3128")
	}
}

func TestGenerateGostConfig_DefaultDeny(t *testing.T) {
	cfg := executor.GenerateGostConfig(nil, nil)
	if !strings.Contains(cfg, "reverse: true") {
		t.Error("config should have reverse: true for whitelist mode")
	}
}

func TestGenerateGostConfig_WithCIDR(t *testing.T) {
	cfg := executor.GenerateGostConfig([]string{"192.168.0.0/16"}, nil)
	if !strings.Contains(cfg, "192.168.0.0/16") {
		t.Error("config should contain the CIDR")
	}
}

func TestGenerateGostConfig_WithHost(t *testing.T) {
	cfg := executor.GenerateGostConfig(nil, []string{"api.github.com"})
	if !strings.Contains(cfg, "api.github.com") {
		t.Error("config should contain the hostname")
	}
}

func TestGenerateGostConfig_BothCIDRAndHost(t *testing.T) {
	cfg := executor.GenerateGostConfig([]string{"10.0.0.0/8"}, []string{"registry.npmjs.org"})
	if !strings.Contains(cfg, "10.0.0.0/8") {
		t.Error("config should contain the CIDR")
	}
	if !strings.Contains(cfg, "registry.npmjs.org") {
		t.Error("config should contain the hostname")
	}
}

func TestNormalizeProjectName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"agent-sandbox", "agent-sandbox"},
		{"MyProject", "myproject"},
		{"my project", "my-project"},
		{"my_project", "my_project"},
		{"123abc", "123abc"},
		{"café", "caf"},
		{"", ""},
		{"-foo", "foo"},
		{"foo-", "foo"},
		{"_bar", "bar"},
		{"---", ""},
		{"  leading space", "leading-space"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := executor.NormalizeProjectName(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeProjectName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNewSandboxProject_NoExternalNetwork(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With gost, there are now 2 networks: default and sandbox_internal
	if _, ok := proj.Networks["default"]; !ok {
		t.Error("Networks should contain \"default\"")
	}
	if _, ok := proj.Networks["sandbox_internal"]; !ok {
		t.Error("Networks should contain \"sandbox_internal\"")
	}
}

func TestNewSandboxProject_WithExternalNetwork(t *testing.T) {
	proj, err := executor.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", nil, nil, "myproject_default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// default + sandbox_internal + myproject_default = 3
	if len(proj.Networks) != 3 {
		t.Errorf("Networks len = %d, want 3", len(proj.Networks))
	}
	extNet, ok := proj.Networks["myproject_default"]
	if !ok {
		t.Fatal("external network \"myproject_default\" not found in project.Networks")
	}
	if !extNet.External {
		t.Error("external network should have External=true")
	}
	svc := proj.Services[executor.SandboxServiceName]
	if _, ok := svc.Networks["myproject_default"]; !ok {
		t.Error("workspace service should be on external network")
	}
	// workspace should still be on "default" too
	if _, ok := svc.Networks["default"]; !ok {
		t.Error("workspace service should still be on \"default\" network")
	}
	gost := proj.Services["gost"]
	if _, ok := gost.Networks["myproject_default"]; ok {
		t.Error("gost should NOT be on external network")
	}
}
