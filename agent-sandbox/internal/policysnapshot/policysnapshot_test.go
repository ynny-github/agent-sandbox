package policysnapshot

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

func TestWriteLoad_RoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := &config.Config{ToolMode: "hook"}
	cfg.Sandbox.Command.Allow = []string{"git *"}
	cfg.Sandbox.Command.Drop = []config.DropRule{
		{Pattern: "git push -f*"},
		{Pattern: "gh *", Message: "gh is disabled"},
	}
	cfg.Sandbox.Command.Network.AllowDomains = []string{"proxy.golang.org"}
	cfg.Sandbox.Command.Host.AllowEnv = []string{"CI"}

	path, cleanup, err := Write(cfg)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	defer cleanup()

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, cfg)
	}
}

func TestWrite_CleanupRemoves(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path, cleanup, err := Write(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup did not remove snapshot: %v", err)
	}
}

func TestLoad_MissingFile_Errors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected error for missing snapshot, got nil")
	}
}

func TestWrite_HonorsXDGStateHome(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	path, cleanup, err := Write(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	want := filepath.Join(base, "agent-sandbox")
	if filepath.Dir(path) != want {
		t.Errorf("snapshot dir = %q, want %q", filepath.Dir(path), want)
	}
}
