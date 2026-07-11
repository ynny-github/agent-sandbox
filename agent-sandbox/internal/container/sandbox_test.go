package container_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/container"
)

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
