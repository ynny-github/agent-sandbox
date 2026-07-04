package container_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/container"
)

func TestNewSandboxProject_Name(t *testing.T) {
	proj, err := container.NewSandboxProject(12345, 1000, 2000, "./ctx", "Dockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := container.ProjectSandboxName(cwd)
	if proj.Name != want {
		t.Errorf("Name = %q, want %q", proj.Name, want)
	}
}

func TestNewSandboxProject_ImageName(t *testing.T) {
	proj, err := container.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := proj.Services[container.SandboxServiceName]
	want := "agent-sandbox/myapp"
	if svc.Image != want {
		t.Errorf("Image = %q, want %q", svc.Image, want)
	}
}

func TestNewSandboxProject_BuildConfig(t *testing.T) {
	proj, err := container.NewSandboxProject(1, 1000, 2000, "./ctx", "MyDockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := proj.Services[container.SandboxServiceName]
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
	proj, err := container.NewSandboxProject(99, 1000, 2000, "./ctx", "Dockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, serviceName := range []string{container.SandboxServiceName} {
		svc := proj.Services[serviceName]
		if svc.Labels["cr.managed"] != "true" {
			t.Errorf("%s label cr.managed = %q, want \"true\"", serviceName, svc.Labels["cr.managed"])
		}
		if svc.Labels["cr.project_dir"] != cwd {
			t.Errorf("%s label cr.project_dir = %q, want %q", serviceName, svc.Labels["cr.project_dir"], cwd)
		}
	}
}

func TestNewSandboxProject_Init(t *testing.T) {
	proj, err := container.NewSandboxProject(99, 1000, 2000, "./ctx", "Dockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, serviceName := range []string{container.SandboxServiceName} {
		svc := proj.Services[serviceName]
		if svc.Init == nil {
			t.Errorf("%s service Init = nil, want non-nil pointer to true", serviceName)
			continue
		}
		if !*svc.Init {
			t.Errorf("%s service Init = %v, want true", serviceName, *svc.Init)
		}
	}
}

func TestNewSandboxProject_VolumeMountsCwd(t *testing.T) {
	proj, err := container.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := proj.Services[container.SandboxServiceName]
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
	proj, err := container.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := proj.Services[container.SandboxServiceName]; !ok {
		t.Errorf("service %q not found in project", container.SandboxServiceName)
	}
}

func TestNewSandboxProject_WorkingDir(t *testing.T) {
	proj, err := container.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cwd, _ := os.Getwd()
	if proj.WorkingDir != cwd {
		t.Errorf("WorkingDir = %q, want %q", proj.WorkingDir, cwd)
	}
}

func TestSandboxServiceName(t *testing.T) {
	if container.SandboxServiceName != "workspace" {
		t.Errorf("SandboxServiceName = %q, want \"workspace\"", container.SandboxServiceName)
	}
}

func TestProjectSandboxName_StableForSameCWD(t *testing.T) {
	cwd := filepath.Join("tmp", "my project")
	got1 := container.ProjectSandboxName(cwd)
	got2 := container.ProjectSandboxName(cwd)
	if got1 != got2 {
		t.Fatalf("ProjectSandboxName not stable: %q != %q", got1, got2)
	}
	if !strings.HasPrefix(got1, "cr-sandbox-my-project-") {
		t.Fatalf("ProjectSandboxName = %q, want cr-sandbox-my-project-*", got1)
	}
}

func TestProjectSandboxName_DifferentPathsWithSameBaseDiffer(t *testing.T) {
	got1 := container.ProjectSandboxName(filepath.Join("tmp", "one", "app"))
	got2 := container.ProjectSandboxName(filepath.Join("tmp", "two", "app"))
	if got1 == got2 {
		t.Fatalf("ProjectSandboxName collision for different paths: %q", got1)
	}
}

func TestProjectSandboxName_NormalizesUnsupportedCharacters(t *testing.T) {
	got := container.ProjectSandboxName(filepath.Join("tmp", "My App!!!"))
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
	proj, err := container.NewSandboxProject(pid, 1000, 2000, "./ctx", "Dockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := container.ProjectSandboxName(cwd)
	if proj.Name != want {
		t.Errorf("Name = %q, want %q", proj.Name, want)
	}
}

func TestNewSandboxProject_User(t *testing.T) {
	proj, err := container.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := proj.Services[container.SandboxServiceName]
	if svc.User != "1000:2000" {
		t.Errorf("User = %q, want \"1000:2000\"", svc.User)
	}
}

func TestNewSandboxProject_RootUserReturnsError(t *testing.T) {
	_, err := container.NewSandboxProject(1, 0, 0, "./ctx", "Dockerfile", "myapp", false, "")
	if err == nil {
		t.Fatal("expected error when uid=0 (root), got nil")
	}
	if !strings.Contains(err.Error(), "root") {
		t.Errorf("error should mention root: %v", err)
	}
}

func TestNewSandboxProject_RootGIDOnlyNotBlocked(t *testing.T) {
	// only uid=0 is blocked, gid=0 alone is allowed
	proj, err := container.NewSandboxProject(1, 1000, 0, "./ctx", "Dockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("gid=0 with non-root uid should be allowed: %v", err)
	}
	svc := proj.Services[container.SandboxServiceName]
	if svc.User != "1000:0" {
		t.Errorf("User = %q, want \"1000:0\"", svc.User)
	}
}

func TestNewSandboxProject_WorkingDir_Container(t *testing.T) {
	proj, err := container.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := proj.Services[container.SandboxServiceName]
	if svc.WorkingDir != "/workspace" {
		t.Errorf("svc.WorkingDir = %q, want \"/workspace\"", svc.WorkingDir)
	}
}

func TestNewSandboxProject_HasSandboxNetwork(t *testing.T) {
	proj, err := container.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	net, ok := proj.Networks["sandbox"]
	if !ok {
		t.Fatal("sandbox network not found in project.Networks")
	}
	if net.Name != proj.Name+"_sandbox" {
		t.Errorf("sandbox network name = %q, want %q", net.Name, proj.Name+"_sandbox")
	}
	if _, ok := proj.Services[container.SandboxServiceName].Networks["sandbox"]; !ok {
		t.Error("workspace should be attached to the sandbox network")
	}
}

func TestNewSandboxProject_IsolatedWhenExternalDenied(t *testing.T) {
	proj, err := container.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !proj.Networks["sandbox"].Internal {
		t.Error("sandbox network should be Internal when allow_external is false")
	}
}

func TestNewSandboxProject_OpenWhenExternalAllowed(t *testing.T) {
	proj, err := container.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", true, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proj.Networks["sandbox"].Internal {
		t.Error("sandbox network should not be Internal when allow_external is true")
	}
}

func TestNewSandboxProject_NoGostService(t *testing.T) {
	proj, err := container.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := proj.Services["gost"]; ok {
		t.Error("gost service should no longer exist")
	}
	if _, ok := proj.Configs["gost_config"]; ok {
		t.Error("gost_config should no longer exist")
	}
}

func TestNewSandboxProject_NoProxyEnv(t *testing.T) {
	proj, err := container.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	env := proj.Services[container.SandboxServiceName].Environment
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"} {
		if _, ok := env[k]; ok {
			t.Errorf("proxy env %q should not be set", k)
		}
	}
}

func TestNewSandboxProject_WithExternalNetwork(t *testing.T) {
	proj, err := container.NewSandboxProject(1, 1000, 2000, "./ctx", "Dockerfile", "myapp", false, "myproject_default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(proj.Networks) != 2 {
		t.Fatalf("Networks len = %d, want 2 (sandbox + external)", len(proj.Networks))
	}
	if _, ok := proj.Services[container.SandboxServiceName].Networks["myproject_default"]; !ok {
		t.Error("workspace should be attached to the external network")
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
			got := container.NormalizeProjectName(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeProjectName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestProjectSandboxName_RelativeAndAbsoluteSamePathMatch(t *testing.T) {
	abs := container.ProjectSandboxName(filepath.Join("/tmp", "proj"))
	viaDotDot := container.ProjectSandboxName(filepath.Join("/tmp", "sub", "..", "proj"))
	if abs != viaDotDot {
		t.Fatalf("ProjectSandboxName should resolve equivalent paths to the same name: %q != %q", abs, viaDotDot)
	}
}
