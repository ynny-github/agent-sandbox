package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ynny-github/agent-sandbox/internal/scaffold"
)

// initHarness stubs the nono call out and points init at a fresh directory.
// The files written are the binary's real embedded ones.
func initHarness(t *testing.T) (dir string, out *bytes.Buffer) {
	t.Helper()
	origValidate := initValidate
	initValidate = func(ctx context.Context, fetched []scaffold.Fetched) ([]string, error) { return nil, nil }
	t.Cleanup(func() { initValidate = origValidate })

	dir = t.TempDir()
	origDir := initDir
	initDir = dir
	t.Cleanup(func() { initDir = origDir })

	out = &bytes.Buffer{}
	initCmd.SetOut(out)
	return dir, out
}

func TestRunInitWritesEveryEmbeddedAsset(t *testing.T) {
	dir, out := initHarness(t)
	if err := runInit(initCmd, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	for _, a := range scaffold.Assets {
		if _, err := os.Stat(filepath.Join(dir, a.Dest)); err != nil {
			t.Errorf("%s not written: %v", a.Dest, err)
		}
	}
	// The written files are the embedded templates, not placeholders: the hook
	// settings must name the execd hook this binary ships.
	settings, err := os.ReadFile(filepath.Join(dir, ".agent-sandbox", "settings.json"))
	if err != nil || !strings.Contains(string(settings), "execd hook claude") {
		t.Errorf("settings.json = %q (err %v), want the embedded template", settings, err)
	}
	if !strings.Contains(out.String(), "wrote .agent-sandbox/settings.json") {
		t.Errorf("report does not list the written file\n%s", out)
	}
}

func TestRunInitSkipsExistingFilesAndStillSucceeds(t *testing.T) {
	dir, out := initHarness(t)
	os.MkdirAll(filepath.Join(dir, ".agent-sandbox"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, ".agent-sandbox", "claude-profile.json"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runInit(initCmd, nil); err != nil {
		t.Fatalf("runInit returned an error for an already-initialized file: %v", err)
	}
	if !strings.Contains(out.String(), "skip") {
		t.Errorf("report does not mention the skip\n%s", out)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".agent-sandbox", "claude-profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "mine" {
		t.Errorf("existing file was overwritten: %q", got)
	}
}

func TestRunInitWritesNothingWhenValidationFails(t *testing.T) {
	dir, _ := initHarness(t)
	initValidate = func(ctx context.Context, fetched []scaffold.Fetched) ([]string, error) {
		return nil, errNotValid
	}
	if err := runInit(initCmd, nil); err == nil {
		t.Fatal("runInit succeeded despite a validation failure")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("init left %d entries behind after a validation failure", len(entries))
	}
}

func TestRunInitPrintsWarningsWithoutFailing(t *testing.T) {
	_, out := initHarness(t)
	initValidate = func(ctx context.Context, fetched []scaffold.Fetched) ([]string, error) {
		return []string{"command-profile.json: [warn] [allow_all_network] command 'ssh' from.git allows unrestricted child network"}, nil
	}
	if err := runInit(initCmd, nil); err != nil {
		t.Fatalf("a warning failed the run: %v", err)
	}
	if !strings.Contains(out.String(), "allow_all_network") {
		t.Errorf("warning not reported\n%s", out)
	}
}

// errNotValid stands in for whatever nono reports; runInit must not write
// regardless of the wording.
var errNotValid = errors.New("command-profile.json: nono profile validate: Result: invalid (1 error)")
