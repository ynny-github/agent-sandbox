//go:build e2e

// Package claude_test's broker_e2e_test.go proves, with a real nono
// installation, that a command run through the broker actually inherits the
// command profile's network policy — the property internal/broker's
// now-deleted network_e2e_test.go used to establish under the old
// per-command NonoExecutor design (see git history around the change that
// deleted the router). It is gated behind the "e2e" build tag rather than
// running under `go test ./...`: it needs a real nono binary, compiles the
// actual agent-sandbox CLI, and reaches out over the network, none of which
// belong in the default, hermetic test run. Run it explicitly with:
//
//	go test -tags e2e ./agent-sandbox/internal/claude/... -run BrokeredCommand -v
package claude_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/claude"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/sandboxhost"
)

// buildAgentSandbox compiles the real CLI binary into dir and returns its
// path. A test's own os.Executable() resolves to the compiled test binary,
// not a cobra-driven CLI that understands `broker --socket`, so
// startCommandBroker itself cannot be exercised here (see its own doc
// comment); this suite drives claude.BrokerArgs — the pure function
// startCommandBroker calls — against a binary that can actually serve the
// `broker` subcommand instead.
//
// dir must be a path the broker session's own profile grants: nono refuses to
// exec a binary out of a directory it has not been told to trust, and unlike
// a real install (under e.g. /usr/local/bin or ~/.local/bin, which nono's own
// baseline already trusts) a throwaway test binary has no such standing grant
// of its own. Passing workdir — already granted by the profile ResolveShell
// builds — sidesteps that without this test inventing a grant it does not
// otherwise need.
func buildAgentSandbox(t *testing.T, dir string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("module root: %v", err)
	}
	out := filepath.Join(dir, "agent-sandbox")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build agent-sandbox: %v\n%s", err, output)
	}
	return out
}

// startBrokerSession starts a real broker inside a real nono run session,
// built exactly as claude.BrokerArgs describes it, and returns the socket
// path. Teardown (SIGTERM, Wait, socket removal) is registered via t.Cleanup.
func startBrokerSession(t *testing.T, nonoPath, selfPath string, cfg *config.Config, workdir string) string {
	t.Helper()
	// A short, unrelated temp dir for the socket: t.TempDir() embeds the
	// (potentially long) test name in the path, which can push a unix socket
	// path past the ~104-byte sun_path limit.
	dir, err := os.MkdirTemp("", "brke2e")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "e.sock")

	args := claude.BrokerArgs(cfg, nonoPath, selfPath, sock, workdir)
	// Captured rather than sent straight to the test binary's own stdout/stderr
	// so a failure to start reports nono's own diagnostic (e.g. "directory is
	// not readable inside the sandbox" for a binary outside every grant)
	// instead of just "socket did not appear".
	var outBuf, errBuf strings.Builder
	cmd := exec.Command(nonoPath, args[1:]...)
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start broker session: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	})

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			return sock
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("broker socket %s did not appear within 15s\nargs: %v\nstdout: %s\nstderr: %s",
		sock, args, outBuf.String(), errBuf.String())
	return ""
}

// runBrokered starts a broker session whose command profile grants
// allowDomains, sends command to it, and returns the exit code.
func runBrokered(t *testing.T, allowDomains []string, command string) int {
	t.Helper()
	nonoPath, err := exec.LookPath("nono")
	if err != nil {
		t.Skip("nono not on PATH")
	}

	workdir := t.TempDir()
	selfPath := buildAgentSandbox(t, workdir)
	cfg := &config.Config{}
	cfg.Sandbox.Shell.AllowDomains = allowDomains
	// sandboxhost.ResolveShell builds the same profile shape an operator would
	// hand-write for the command profile (Task 1's CommandProfilePath): a
	// developer network policy plus whatever domains this case adds. Reusing
	// it here is just a convenient way to produce a valid profile; production
	// profiles are operator-authored, not generated.
	resolved, rerr := sandboxhost.ResolveShell(cfg, workdir)
	if rerr != nil {
		t.Fatalf("ResolveShell: %v", rerr)
	}
	profilePath, cleanupProfile, werr := resolved.WriteProfile()
	if werr != nil {
		t.Fatalf("WriteProfile: %v", werr)
	}
	t.Cleanup(cleanupProfile)
	cfg.CommandProfile = profilePath

	sock := startBrokerSession(t, nonoPath, selfPath, cfg, workdir)

	// The client sends its own working directory as the request's Cwd (see
	// broker.Client.RunCommand's workingDir helper), so this test's cwd must
	// be the directory the profile actually grants. Chdir affects the whole
	// process, so it is undone once this case finishes.
	prevWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(prevWd) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out, errb strings.Builder
	code, rcErr := broker.NewClient(sock).RunCommand(ctx, command, nil, &out, &errb)
	if rcErr != nil {
		t.Fatalf("RunCommand: %v (stderr=%s)", rcErr, errb.String())
	}
	return code
}

func curlCmd(url string) string {
	return "curl -sS -o /dev/null --max-time 15 " + url
}

func TestBrokeredCommand_DeveloperPresetDomainAllowed(t *testing.T) {
	// registry.npmjs.org is in the developer network profile's default allow
	// list (nono's own built-in preset, not one agent-sandbox defines).
	if code := runBrokered(t, nil, curlCmd("https://registry.npmjs.org/")); code != 0 {
		t.Errorf("curl to a preset domain exited %d, want 0", code)
	}
}

func TestBrokeredCommand_DomainOutsidePresetBlocked(t *testing.T) {
	if code := runBrokered(t, nil, curlCmd("https://example.org/")); code == 0 {
		t.Error("curl to a domain outside the preset exited 0, want non-zero")
	}
}

func TestBrokeredCommand_AllowDomainsGrantsAccess(t *testing.T) {
	if code := runBrokered(t, []string{"example.org"}, curlCmd("https://example.org/")); code != 0 {
		t.Errorf("curl to an allow_domains entry exited %d, want 0", code)
	}
}
