package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up from the working directory until it finds go.mod. The
// templates live at the repository root, not beside this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

// TestTemplatesValidate is what lets init fetch from main: a template that
// nono rejects must fail here before it can be merged.
func TestTemplatesValidate(t *testing.T) {
	if _, err := exec.LookPath("nono"); err != nil {
		t.Skip("nono not on PATH")
	}
	root := repoRoot(t)
	for _, name := range []string{"command-profile.json", "claude-profile.json"} {
		path := filepath.Join(root, "templates", "minimal", name)
		out, err := exec.Command("nono", "profile", "validate", path).CombinedOutput()
		if err != nil {
			t.Errorf("nono profile validate %s: %v\n%s", name, err, out)
			continue
		}
		if !strings.Contains(string(out), "Result: valid") {
			t.Errorf("%s: nono did not report the profile valid\n%s", name, out)
		}
	}
}

// TestTemplateConfigNamesBothProfiles keeps the TOML template in step with the
// two JSON files beside it.
func TestTemplateConfigNamesBothProfiles(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "templates", "minimal", "agent-sandbox.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`command_profile = "command-profile.json"`, `profile = "claude-profile.json"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("template config missing %q\n%s", want, body)
		}
	}
}

// TestCommandTemplateNeverForwardsTheExecdSocket is the one grant that turns a
// command into a host-side fork bomb. The environment block is also what makes
// the environment an allowlist at all, so its absence is equally a failure.
func TestCommandTemplateNeverForwardsTheExecdSocket(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "templates", "minimal", "command-profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `"allow_vars"`) {
		t.Fatal("command template declares no allow_vars; nono would forward the whole environment")
	}
	// The comment block names the variable on purpose; only a JSON string
	// entry would forward it.
	if strings.Contains(text, `"AGENT_SANDBOX_EXECD_SOCKET"`) {
		t.Error("command template forwards AGENT_SANDBOX_EXECD_SOCKET")
	}
}
