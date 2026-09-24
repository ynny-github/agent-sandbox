package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "agent-sandbox.toml")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	// validate requires both profiles on disk; write the default names
	// beside the config so these fixtures keep exercising the default paths.
	for _, name := range []string{"command-profile.json", "claude-profile.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestRunExplain_RendersConfig(t *testing.T) {
	cfgPath := writeTempConfig(t, "")
	orig := configPath
	configPath = cfgPath
	t.Cleanup(func() { configPath = orig })

	var buf bytes.Buffer
	explainCmd.SetOut(&buf)
	if err := runExplain(explainCmd, nil); err != nil {
		t.Fatalf("runExplain: %v", err)
	}
	for _, want := range []string{
		"# agent-sandbox environment",
		cfgPath,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\n%s", want, buf.String())
		}
	}
}

func TestRunExplain_MissingConfig(t *testing.T) {
	orig := configPath
	configPath = "/nonexistent/path.toml"
	t.Cleanup(func() { configPath = orig })
	if err := runExplain(explainCmd, nil); err == nil {
		t.Fatal("expected config error, got nil")
	}
}

func runConfigCheckWith(t *testing.T, body string) (string, error) {
	t.Helper()
	orig := configPath
	configPath = writeTempConfig(t, body)
	t.Cleanup(func() { configPath = orig })

	var buf bytes.Buffer
	configCheckCmd.SetOut(&buf)
	err := runConfigCheck(configCheckCmd, nil)
	return buf.String(), err
}

func TestRunConfigCheck_ValidConfig(t *testing.T) {
	restore := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("valid"), nil
	})
	defer restore()

	out, err := runConfigCheckWith(t, "")
	if err != nil {
		t.Fatalf("runConfigCheck: %v", err)
	}
	if !strings.Contains(out, "ok:") {
		t.Errorf("output does not confirm the config resolves:\n%s", out)
	}
}

func TestRunConfigCheck_BrokenToml(t *testing.T) {
	if _, err := runConfigCheckWith(t, "command_profile = \n"); err == nil {
		t.Fatal("expected an error for unparseable TOML, got nil")
	}
}

// config-check hands both profiles to nono rather than describing them: the
// only authority on what a profile grants is nono itself.
func TestRunConfigCheck_ValidatesBothProfilesWithNono(t *testing.T) {
	var validated []string
	restore := stubRunCommand(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "nono" && len(args) == 3 && args[0] == "profile" && args[1] == "validate" {
			validated = append(validated, args[2])
		}
		return []byte("valid"), nil
	})
	defer restore()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"command-profile.json", "claude-profile.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	orig := configPath
	configPath = cfgPath
	t.Cleanup(func() { configPath = orig })

	var buf bytes.Buffer
	configCheckCmd.SetOut(&buf)
	if err := runConfigCheck(configCheckCmd, nil); err != nil {
		t.Fatalf("runConfigCheck: %v", err)
	}
	for _, want := range []string{
		filepath.Join(dir, "claude-profile.json"),
		filepath.Join(dir, "command-profile.json"),
	} {
		if !slices.Contains(validated, want) {
			t.Errorf("nono profile validate was not called for %q; called for %v", want, validated)
		}
	}
	if !strings.Contains(buf.String(), "nono profile show") {
		t.Errorf("output must point at the command that resolves a profile:\n%s", buf.String())
	}
}

func TestRunConfigCheck_FailsWhenTheAgentProfileIsMissing(t *testing.T) {
	restore := stubRunCommand(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("valid"), nil
	})
	defer restore()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent-sandbox.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "command-profile.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := configPath
	configPath = cfgPath
	t.Cleanup(func() { configPath = orig })

	configCheckCmd.SetOut(&bytes.Buffer{})
	err := runConfigCheck(configCheckCmd, nil)
	if err == nil {
		t.Fatal("expected an error when the agent profile is missing, got nil")
	}
	if !strings.Contains(err.Error(), "claude-profile.json") {
		t.Errorf("error must name the missing file: %v", err)
	}
}
