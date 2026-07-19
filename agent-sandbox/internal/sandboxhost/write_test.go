package sandboxhost

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
)

func TestWriteProfile_WritesAndCleans(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sandbox.Host = config.HostConfig{Capabilities: []string{"go"}}
	r, err := Resolve(cfg, "claude")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	path, cleanup, err := r.WriteProfile()
	if err != nil {
		t.Fatalf("WriteProfile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("file is not valid JSON: %v", err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup did not remove the file; stat err = %v", err)
	}
}
