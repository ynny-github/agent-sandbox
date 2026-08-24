//go:build e2e

package broker_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxhost"
)

// runBrokered starts a broker with a command profile built from allowDomains,
// runs argv through it, and returns the exit code.
func runBrokered(t *testing.T, allowDomains []string, argv []string) int {
	t.Helper()

	nonoPath, err := exec.LookPath("nono")
	if err != nil {
		t.Skip("nono not on PATH")
	}

	cfg := &config.Config{}
	cfg.Sandbox.Command.Network.AllowDomains = allowDomains

	workdir := t.TempDir()
	resolved, err := sandboxhost.ResolveCommand(cfg, workdir)
	if err != nil {
		t.Fatalf("ResolveCommand() error = %v", err)
	}
	profilePath, cleanupProfile, err := resolved.WriteProfile()
	if err != nil {
		t.Fatalf("WriteProfile() error = %v", err)
	}
	t.Cleanup(cleanupProfile)

	// t.TempDir() embeds the (potentially long) test name in the path, which
	// on macOS can push a unix socket path past the ~104-byte sun_path limit
	// and make bind(2) fail with "invalid argument". Use a short, unrelated
	// temp dir for the socket instead (same pattern as server_test.go).
	dir, err := os.MkdirTemp("", "brk")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	// The client sends its own working directory, which the executor validates
	// against the directory it was given, so pass the test process's cwd here.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}

	sock := filepath.Join(dir, "e.sock")
	srv, err := broker.NewServer(sock,
		broker.NewNonoExecutor(nonoPath, profilePath, cwd, resolved.EnvAllowVars()))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })

	var out, errb testBuffer
	code, err := broker.NewClient(sock).RunSandboxed(
		context.Background(), argv, nil, &out, &errb)
	if err != nil {
		t.Fatalf("RunSandboxed() error = %v (stderr: %s)", err, errb.String())
	}
	return code
}

func TestBrokeredCommand_DeveloperPresetDomainAllowed(t *testing.T) {
	// registry.npmjs.org is in the developer preset's package_registries group.
	if code := runBrokered(t, nil, curlArgs("https://registry.npmjs.org/")); code != 0 {
		t.Errorf("curl to a preset domain exited %d, want 0", code)
	}
}

func TestBrokeredCommand_DomainOutsidePresetBlocked(t *testing.T) {
	if code := runBrokered(t, nil, curlArgs("https://example.org/")); code == 0 {
		t.Error("curl to a domain outside the preset exited 0, want non-zero")
	}
}

func TestBrokeredCommand_AllowDomainsGrantsAccess(t *testing.T) {
	if code := runBrokered(t, []string{"example.org"}, curlArgs("https://example.org/")); code != 0 {
		t.Errorf("curl to an allow_domains entry exited %d, want 0", code)
	}
}

func curlArgs(url string) []string {
	return []string{"curl", "-sS", "-o", "/dev/null", "--max-time", "15", url}
}
