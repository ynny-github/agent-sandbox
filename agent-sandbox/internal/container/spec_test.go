package container_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/container"
)

func TestNewSandboxSpec(t *testing.T) {
	spec, err := container.NewSandboxSpec(1000, 2000, "./ctx", "MyDockerfile", "myapp", false, "proj_default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cwd, _ := os.Getwd()

	if want := container.ProjectSandboxName(cwd); spec.Name != want {
		t.Errorf("Name = %q, want %q", spec.Name, want)
	}
	if spec.ImageTag != "agent-sandbox/myapp" {
		t.Errorf("ImageTag = %q, want agent-sandbox/myapp", spec.ImageTag)
	}
	if want := spec.Name + "_sandbox"; spec.NetworkName != want {
		t.Errorf("NetworkName = %q, want %q", spec.NetworkName, want)
	}
	if !spec.Internal {
		t.Error("Internal should be true when allowExternal=false")
	}
	if spec.ExternalNetwork != "proj_default" {
		t.Errorf("ExternalNetwork = %q, want proj_default", spec.ExternalNetwork)
	}
	absCtx, _ := filepath.Abs("./ctx")
	if spec.BuildContext != absCtx {
		t.Errorf("BuildContext = %q, want %q", spec.BuildContext, absCtx)
	}
	if spec.Dockerfile != "MyDockerfile" {
		t.Errorf("Dockerfile = %q", spec.Dockerfile)
	}
	if spec.Labels[container.LabelManaged] != "true" || spec.Labels[container.LabelProjectDir] != cwd {
		t.Errorf("Labels = %v", spec.Labels)
	}
	if spec.UID != 1000 || spec.GID != 2000 {
		t.Errorf("UID/GID = %d/%d", spec.UID, spec.GID)
	}
}

func TestNewSandboxSpec_AllowExternalNotInternal(t *testing.T) {
	spec, err := container.NewSandboxSpec(1, 1, "./ctx", "Dockerfile", "img", true, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Internal {
		t.Error("Internal should be false when allowExternal=true")
	}
}

func TestNewSandboxSpec_RejectsRootUID(t *testing.T) {
	if _, err := container.NewSandboxSpec(0, 0, "./ctx", "Dockerfile", "img", false, ""); err == nil {
		t.Fatal("expected error for uid 0")
	}
}
