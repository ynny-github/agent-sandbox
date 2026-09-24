//go:build e2e

// This file proves, with a real nono and a real node, that the agent profile
// in this repository actually delivers the context-mode backend selection into
// the sandbox — the one fact no hermetic test can establish, because it
// depends on a profile this repository deliberately never reads.
//
// Run it explicitly:
//
//	go test -tags e2e ./internal/claude/... -run ContextModeProbe -v
package claude

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ynny-github/agent-sandbox/internal/execd"
)

func TestContextModeProbe_AgainstTheRepoProfile(t *testing.T) {
	if _, err := exec.LookPath("nono"); err != nil {
		t.Skip("nono not on PATH")
	}
	profile, err := filepath.Abs("../../claude-profile.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(profile); serr != nil {
		t.Skipf("agent profile not found at %s", profile)
	}
	t.Setenv(ContextModeEnvVar, ContextModeExecd)
	t.Setenv(execd.SocketEnvVar, "/tmp/agent-sandbox-e2e-probe.sock")

	// The probe grants nono the working directory with --allow-cwd, and a real
	// launch runs from the project root. `go test` runs from the package
	// directory instead, which is narrower than what the launcher ever grants:
	// a toolchain manager that walks upward for its config (mise reads the
	// repo's .mise.toml) is then denied the read and node never starts.
	// Measured: the same probe passes from the repo root and fails from this
	// package's directory, against an identical profile. Chdir so the test
	// measures the profile rather than `go test`'s cwd.
	t.Chdir(filepath.Dir(profile))

	if perr := probeContextMode(profile); perr != nil {
		t.Fatalf("the repo's agent profile does not satisfy the context-mode probe: %v", perr)
	}
}
