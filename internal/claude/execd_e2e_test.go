//go:build e2e

// Package claude_test's execd_e2e_test.go proves, with a real nono
// installation, that a command run through execd actually inherits the
// command profile's network policy — the property internal/execd's
// now-deleted network_e2e_test.go used to establish under the old
// per-command NonoExecutor design (see git history around the change that
// deleted the router). It is gated behind the "e2e" build tag rather than
// running under `go test ./...`: it needs a real nono binary, compiles the
// actual agent-sandbox CLI, and reaches out over the network, none of which
// belong in the default, hermetic test run. Run it explicitly with:
//
//	go test -tags e2e ./internal/claude/... -run ExecdCommand -v
package claude_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/internal/claude"
	"github.com/ynny-github/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/internal/execd"
)

// buildAgentSandbox compiles the real CLI binary into dir and returns its
// path. A test's own os.Executable() resolves to the compiled test binary,
// not a cobra-driven CLI that understands `execd --socket`, so
// startExecd itself cannot be exercised here (see its own doc
// comment); this suite drives claude.ExecdArgs — the pure function
// startExecd calls — against a binary that can actually serve the
// `execd` subcommand instead.
//
// dir must be a path the execd session's own profile grants: nono refuses to
// exec a binary out of a directory it has not been told to trust, and unlike
// a real install (under e.g. /usr/local/bin or ~/.local/bin, which nono's own
// baseline already trusts) a throwaway test binary has no such standing grant
// of its own. Passing workdir — already granted by writeFixtureProfile's
// filesystem.allow — sidesteps that without this test inventing a grant it
// does not otherwise need.
func buildAgentSandbox(t *testing.T, dir string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("module root: %v", err)
	}
	out := filepath.Join(dir, "agent-sandbox")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/agent-sandbox")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build agent-sandbox: %v\n%s", err, output)
	}
	return out
}

// startExecdSession starts a real execd inside a real nono run session,
// built exactly as claude.ExecdArgs describes it, and returns the socket
// path. Teardown (SIGTERM, Wait, socket removal) is registered via t.Cleanup.
func startExecdSession(t *testing.T, nonoPath, selfPath string, cfg *config.Config, workdir string) string {
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

	args := claude.ExecdArgs(cfg, nonoPath, selfPath, sock, workdir)
	// Captured rather than sent straight to the test binary's own stdout/stderr
	// so a failure to start reports nono's own diagnostic (e.g. "directory is
	// not readable inside the sandbox" for a binary outside every grant)
	// instead of just "socket did not appear".
	var outBuf, errBuf strings.Builder
	cmd := exec.Command(nonoPath, args[1:]...)
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start execd session: %v", err)
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
	t.Fatalf("execd socket %s did not appear within 15s\nargs: %v\nstdout: %s\nstderr: %s",
		sock, args, outBuf.String(), errBuf.String())
	return ""
}

// fixtureProfile is the subset of nono's own profile schema this suite needs:
// a filesystem grant for the working directory, an env baseline, and a
// network section. agent-sandbox generates no profile at all any more — the
// operator writes both the agent's and execd's in nono's own
// schema, and agent-sandbox only names them. This suite hand-writes its
// fixture command profile the same way a real operator would.
type fixtureProfile struct {
	Meta        fixtureMeta        `json:"meta"`
	Groups      *fixtureGroups     `json:"groups,omitempty"`
	Filesystem  fixtureFilesystem  `json:"filesystem"`
	Environment fixtureEnvironment `json:"environment"`
	Network     *fixtureNetwork    `json:"network,omitempty"`
}

type fixtureMeta struct {
	Name string `json:"name"`
}

type fixtureGroups struct {
	Include []string `json:"include,omitempty"`
}

type fixtureFilesystem struct {
	Allow     []string `json:"allow,omitempty"`
	AllowFile []string `json:"allow_file,omitempty"`
}

type fixtureEnvironment struct {
	AllowVars []string `json:"allow_vars,omitempty"`
}

type fixtureNetwork struct {
	NetworkProfile string   `json:"network_profile,omitempty"`
	AllowDomain    []string `json:"allow_domain,omitempty"`
}

// writeFixtureProfile writes the nono profile this suite's execd session
// runs under to a temp file and returns its path (removed via t.Cleanup):
// workdir read+write, "/dev/null" allow-listed, nix_runtime/git_config
// (harmless where their paths do not exist, required where they do), and the
// fixed "developer" network preset plus allowDomains.
func writeFixtureProfile(t *testing.T, workdir string, allowDomains []string) string {
	t.Helper()
	p := fixtureProfile{
		Meta:       fixtureMeta{Name: "execd e2e"},
		Groups:     &fixtureGroups{Include: []string{"git_config", "nix_runtime"}},
		Filesystem: fixtureFilesystem{Allow: []string{workdir}, AllowFile: []string{"/dev/null"}},
		Environment: fixtureEnvironment{
			AllowVars: []string{"HOME", "LANG", "LC_ALL", "PATH", "TERM", "USER"},
		},
		Network: &fixtureNetwork{NetworkProfile: "developer", AllowDomain: allowDomains},
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal fixture profile: %v", err)
	}
	f, err := os.CreateTemp("", "execd-e2e-profile-*.json")
	if err != nil {
		t.Fatalf("create profile temp file: %v", err)
	}
	path := f.Name()
	t.Cleanup(func() { os.Remove(path) })
	if _, err := f.Write(data); err != nil {
		f.Close()
		t.Fatalf("write fixture profile: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close fixture profile: %v", err)
	}
	return path
}

// runExecded starts an execd session whose command profile grants
// allowDomains, sends command to it, and returns the exit code.
func runExecded(t *testing.T, allowDomains []string, command string) int {
	t.Helper()
	nonoPath, err := exec.LookPath("nono")
	if err != nil {
		t.Skip("nono not on PATH")
	}

	workdir := t.TempDir()
	selfPath := buildAgentSandbox(t, workdir)
	cfg := &config.Config{CommandProfile: writeFixtureProfile(t, workdir, allowDomains)}

	sock := startExecdSession(t, nonoPath, selfPath, cfg, workdir)

	// The client sends its own working directory as the request's Cwd (see
	// execd.Client.RunCommand's workingDir helper), so this test's cwd must
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
	// The command runs on these descriptors directly; nothing of its output
	// travels back over the socket, so what the case asserts on is read out of
	// the file afterwards.
	errFile, err := os.CreateTemp(t.TempDir(), "err")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { errFile.Close() })
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { devnull.Close() })

	code, rcErr := execd.NewClient(sock).RunCommand(ctx, command,
		execd.Stdio{In: devnull, Out: errFile, Err: errFile}, execd.RunOptions{})
	if rcErr != nil {
		b, _ := os.ReadFile(errFile.Name())
		t.Fatalf("RunCommand: %v (output=%s)", rcErr, b)
	}
	return code
}

func curlCmd(url string) string {
	return "curl -sS -o /dev/null --max-time 15 " + url
}

func TestExecdCommand_DeveloperPresetDomainAllowed(t *testing.T) {
	// registry.npmjs.org is in the developer network profile's default allow
	// list (nono's own built-in preset, not one agent-sandbox defines).
	if code := runExecded(t, nil, curlCmd("https://registry.npmjs.org/")); code != 0 {
		t.Errorf("curl to a preset domain exited %d, want 0", code)
	}
}

func TestExecdCommand_DomainOutsidePresetBlocked(t *testing.T) {
	if code := runExecded(t, nil, curlCmd("https://example.org/")); code == 0 {
		t.Error("curl to a domain outside the preset exited 0, want non-zero")
	}
}

func TestExecdCommand_AllowDomainsGrantsAccess(t *testing.T) {
	if code := runExecded(t, []string{"example.org"}, curlCmd("https://example.org/")); code != 0 {
		t.Errorf("curl to an allow_domains entry exited %d, want 0", code)
	}
}
