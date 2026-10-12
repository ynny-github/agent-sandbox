# execd-Owned Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** execd starts, reuses, stops and garbage-collects its own nono session (`execd up` / `down` / `gc`), and the launcher only calls `up` and `down`.

**Architecture:** A new `internal/execd/session.go` owns the session directory (`$XDG_STATE_HOME/execd/<key>/`), the nono argv, start/stop, and gc; the start/stop machinery moves there from `internal/claude/launch.go`. `serve` touches `last-used` per request. `cmd/execd` gains three subcommands. The launcher runs the `execd` binary it already resolves and checks.

**Tech Stack:** Go 1.25 stdlib (`syscall.Getsid`, `syscall.Flock`, `crypto/sha256`), nono 0.78.

**Spec:** `docs/superpowers/specs/2026-10-12-execd-session-design.md`

## Global Constraints

- Session key: first 12 hex digits of SHA-256 over `sid "\x00" workdir "\x00" profile`; sid is `getsid(0)`; workdir and profile are absolute paths, symlinks not resolved.
- Session root: `$XDG_STATE_HOME/execd/` (`~/.local/state/execd/` when unset); session dir `<root>/<key>/`, mode 0700; lock `<root>/<key>.lock`.
- Session files: `execd.sock`, `pid`, `owner`, `profile.sha256`, `log`, `last-used`.
- `up`: `--profile` required, `--workdir` optional (default: current directory, made absolute, always passed to nono explicitly). Start timeout 15 s; failure message carries the last 20 lines of `log`.
- `down`: refuses any path that is not `<root>/<key>/execd.sock`; a session whose socket does not accept a connection is removed without signalling its pid; stop timeout 5 s, then SIGKILL.
- `gc --idle` default `168h`; `--dry-run` changes nothing. Nothing runs gc implicitly.
- Exit status: 0 success, 1 runtime failure, 2 usage. No config file. `internal/execd` imports no agent-sandbox package.
- The launcher keeps `ResolveExecd` and the command-profile writability check of execd and its directory.
- Commits follow Conventional Commits (`.claude/rules/git-commit.md`).

## Review Focus

1. A launcher killed with SIGKILL leaves its session running; `execd gc` must collect it once its shell is gone (pinned in Task 4, `TestGC_DownsASessionWhoseOwnerIsGone`).
2. `down` given a stale session whose pid now belongs to an unrelated live process must not signal it (pinned in Task 2, `TestDown_DeadSessionIsRemovedWithoutSignalling`, which uses the test process's own pid).
3. Liveness probes from `up`/`down`/`gc` connect to the socket; they must not count as use, or `gc` never sees a session idle (pinned in Task 3, `TestServe_AProbeConnectionDoesNotCountAsUse`).
4. `down ../../somewhere/execd.sock` or any path outside the root must be refused before anything is removed (pinned in Task 2, `TestDown_RefusesAPathOutsideTheRoot`).
5. Two `up` calls for the same key racing must yield one session, not two nono processes (pinned in Task 1, `TestUp_ConcurrentCallsStartOneSession`).

---

### Task 1: `execd.Up` and the session directory

**Files:**
- Create: `internal/execd/session.go`
- Create: `internal/execd/session_test.go` (package `execd`, holds the package's `TestMain`)

**Interfaces:**
- Produces: `func Root() (string, error)`; `func SessionSocket(profile, workdir string) (string, error)`; `func Up(profile, workdir string) (string, error)`; unexported `callerSession func() (int, error)`, `startTimeout time.Duration`, `alive(dir string) bool`, `processExists(pid int) bool`, `lockKey(dir string) (func(), error)`, `const sockName = "execd.sock"`.

- [ ] **Step 1: Write the failing tests**

Create `internal/execd/session_test.go`:

```go
package execd

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// TestMain lets the test binary stand in for execd under a fake nono: Up
// starts `<os.Executable()> serve --socket <path>`, which here is this
// binary. EXECD_TEST_WEDGED makes it ignore SIGTERM, for Down's escalation.
func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "serve" && os.Args[2] == "--socket" {
		srv, err := NewServer(os.Args[3], NewShellExecutor())
		if err != nil {
			os.Exit(1)
		}
		if os.Getenv("EXECD_TEST_WEDGED") != "" {
			signal.Ignore(syscall.SIGTERM)
			srv.Serve()
			os.Exit(0)
		}
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGTERM)
		go func() { <-sigs; srv.Close() }()
		srv.Serve()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// passthroughNono records its argv in <dir>/calls, then runs what follows --.
const passthroughNono = "#!/bin/sh\n" +
	"echo \"$@\" >> \"$(dirname \"$0\")/calls\"\n" +
	"while [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n"

// rejectingNono stands in for nono refusing the profile.
const rejectingNono = "#!/bin/sh\necho 'profile rejected' >&2\nexit 1\n"

// fakeNono puts a nono script first on PATH and returns its directory.
func fakeNono(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nono"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// stateHome points XDG_STATE_HOME at a short temp dir: socket paths are
// capped near 104 bytes, and t.TempDir embeds the test name.
func stateHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "xd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("XDG_STATE_HOME", dir)
	return dir
}

func writeProfile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "command-profile.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func withSession(t *testing.T, sid int) {
	t.Helper()
	prev := callerSession
	callerSession = func() (int, error) { return sid, nil }
	t.Cleanup(func() { callerSession = prev })
}

// killSession SIGKILLs a session started by a test. It is test cleanup, not
// Down: Down is Task 2's subject and must not be what keeps other tests tidy.
func killSession(sock string) {
	if pid, err := readInt(filepath.Join(filepath.Dir(sock), "pid")); err == nil {
		syscall.Kill(pid, syscall.SIGKILL)
	}
}

// upForTest runs Up and registers killSession for cleanup.
func upForTest(t *testing.T, profile string) string {
	t.Helper()
	sock, err := Up(profile, t.TempDir())
	if err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	t.Cleanup(func() { killSession(sock) })
	return sock
}

func countCalls(t *testing.T, nonoDir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(nonoDir, "calls"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// deadPID returns the pid of a process that has exited and been reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestUp_StartsAndReturnsALiveSocket(t *testing.T) {
	stateHome(t)
	fakeNono(t, passthroughNono)
	sock := upForTest(t, writeProfile(t, "{}"))

	root, _ := Root()
	if filepath.Dir(filepath.Dir(sock)) != root || filepath.Base(sock) != sockName {
		t.Errorf("socket %s is not <root>/<key>/%s under %s", sock, sockName, root)
	}
	if !alive(filepath.Dir(sock)) {
		t.Error("the session is not alive after Up")
	}
	info, err := os.Stat(filepath.Dir(sock))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("session dir mode = %v (err %v), want 0700", info.Mode().Perm(), err)
	}
	for _, f := range []string{"pid", "owner", "profile.sha256", "log", "last-used"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(sock), f)); err != nil {
			t.Errorf("session file %s missing: %v", f, err)
		}
	}
}

func TestUp_SecondCallReusesTheSession(t *testing.T) {
	stateHome(t)
	nono := fakeNono(t, passthroughNono)
	profile := writeProfile(t, "{}")
	workdir := t.TempDir()

	first, err := Up(profile, workdir)
	if err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	t.Cleanup(func() { killSession(first) })
	second, err := Up(profile, workdir)
	if err != nil {
		t.Fatalf("second Up() error = %v", err)
	}
	if first != second {
		t.Errorf("second Up() = %s, want the running session's %s", second, first)
	}
	if n := countCalls(t, nono); n != 1 {
		t.Errorf("nono started %d times, want 1", n)
	}
}

func TestUp_ConcurrentCallsStartOneSession(t *testing.T) {
	stateHome(t)
	nono := fakeNono(t, passthroughNono)
	profile := writeProfile(t, "{}")
	workdir := t.TempDir()

	var wg sync.WaitGroup
	socks := make([]string, 4)
	for i := range socks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := Up(profile, workdir)
			if err != nil {
				t.Errorf("Up() error = %v", err)
			}
			socks[i] = s
		}(i)
	}
	wg.Wait()
	t.Cleanup(func() { killSession(socks[0]) })
	for _, s := range socks[1:] {
		if s != socks[0] {
			t.Errorf("concurrent Up() returned %s and %s", socks[0], s)
		}
	}
	if n := countCalls(t, nono); n != 1 {
		t.Errorf("nono started %d times, want 1", n)
	}
}

func TestUp_ChangedProfileFails(t *testing.T) {
	stateHome(t)
	fakeNono(t, passthroughNono)
	profile := writeProfile(t, "{}")
	workdir := t.TempDir()
	sock, err := Up(profile, workdir)
	if err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	t.Cleanup(func() { killSession(sock) })

	if err := os.WriteFile(profile, []byte(`{"changed":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Up(profile, workdir)
	if err == nil || !strings.Contains(err.Error(), "execd down "+sock) {
		t.Fatalf("Up() after a profile change error = %v, want one naming `execd down %s`", err, sock)
	}
}

func TestUp_ReplacesADeadLeftover(t *testing.T) {
	stateHome(t)
	nono := fakeNono(t, passthroughNono)
	profile := writeProfile(t, "{}")
	workdir := t.TempDir()
	sock, err := SessionSocket(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(deadPID(t))), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Up(profile, workdir)
	if err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	t.Cleanup(func() { killSession(got) })
	if got != sock || !alive(dir) || countCalls(t, nono) != 1 {
		t.Errorf("Up() = %s alive=%v starts=%d, want a fresh session at %s", got, alive(dir), countCalls(t, nono), sock)
	}
}

func TestSessionSocket_DependsOnTheCallerSession(t *testing.T) {
	stateHome(t)
	profile := writeProfile(t, "{}")
	workdir := t.TempDir()
	withSession(t, 1001)
	a, _ := SessionSocket(profile, workdir)
	withSession(t, 1002)
	b, _ := SessionSocket(profile, workdir)
	if a == b {
		t.Errorf("two caller sessions share socket %s", a)
	}
}

func TestUp_ChildExitingEarlyFailsWithTheLog(t *testing.T) {
	stateHome(t)
	fakeNono(t, rejectingNono)
	profile := writeProfile(t, "{}")
	workdir := t.TempDir()

	_, err := Up(profile, workdir)
	if err == nil || !strings.Contains(err.Error(), "profile rejected") {
		t.Fatalf("Up() error = %v, want it to carry nono's log", err)
	}
	sock, _ := SessionSocket(profile, workdir)
	if _, statErr := os.Stat(filepath.Dir(sock)); !os.IsNotExist(statErr) {
		t.Errorf("session dir left behind after a failed Up (stat err %v)", statErr)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/execd/ -run 'Up_|SessionSocket'`
Expected: FAIL to compile — `undefined: callerSession`, `undefined: Up`, `undefined: Root`.

- [ ] **Step 3: Implement**

Create `internal/execd/session.go`. Move `waitForSocketOrExit` (with its doc comment) and the `ExecdArgs` doc comment from `internal/claude/launch.go` into it (adapt the wording from "the launcher" to "Up"); do not delete them from launch.go yet — Task 6 does.

```go
package execd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// sockName is the socket's file name inside a session directory.
const sockName = "execd.sock"

// startTimeout bounds how long Up waits for the socket. A sandbox that
// cannot start fails here rather than leaving its caller waiting on an execd
// that will never answer.
const startTimeout = 15 * time.Second

// callerSession is getsid(0): the session of the process running Up, which is
// the interactive shell's pid for everything started from that shell. A var so
// tests can stand in two shells.
var callerSession = func() (int, error) { return syscall.Getsid(0) }

// Root is the directory every session lives under: execd's own, never shared
// with another tool's state, because a socket's bind grant reaches its parent
// directory.
func Root() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "execd"), nil
}

// sessionDir names the session for (caller session, workdir, profile).
// profile and workdir must already be absolute.
func sessionDir(profile, workdir string) (dir string, sid int, err error) {
	sid, err = callerSession()
	if err != nil {
		return "", 0, fmt.Errorf("session id: %w", err)
	}
	root, err := Root()
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", sid, workdir, profile)))
	return filepath.Join(root, hex.EncodeToString(sum[:])[:12]), sid, nil
}

func absPaths(profile, workdir string) (string, string, error) {
	p, err := filepath.Abs(profile)
	if err != nil {
		return "", "", err
	}
	if workdir == "" {
		if workdir, err = os.Getwd(); err != nil {
			return "", "", err
		}
	}
	w, err := filepath.Abs(workdir)
	return p, w, err
}

// SessionSocket is the socket Up would use for profile and workdir, computed
// without starting or touching anything.
func SessionSocket(profile, workdir string) (string, error) {
	p, w, err := absPaths(profile, workdir)
	if err != nil {
		return "", err
	}
	dir, _, err := sessionDir(p, w)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sockName), nil
}

// Up starts the session for (caller session, workdir, profile), or returns the
// running one's socket. workdir "" means the current directory.
func Up(profile, workdir string) (string, error) {
	profile, workdir, err := absPaths(profile, workdir)
	if err != nil {
		return "", err
	}
	sum, err := fileSHA256(profile)
	if err != nil {
		return "", fmt.Errorf("read profile: %w", err)
	}
	nono, err := exec.LookPath("nono")
	if err != nil {
		return "", fmt.Errorf("nono not found in PATH: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate execd: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
		self = resolved
	}
	dir, sid, err := sessionDir(profile, workdir)
	if err != nil {
		return "", err
	}
	unlock, err := lockKey(dir)
	if err != nil {
		return "", err
	}
	defer unlock()

	sock := filepath.Join(dir, sockName)
	if _, statErr := os.Stat(dir); statErr == nil {
		if alive(dir) {
			running, _ := os.ReadFile(filepath.Join(dir, "profile.sha256"))
			if string(running) == sum {
				return sock, nil
			}
			return "", fmt.Errorf("the session at %s was started with a different %s; "+
				"run `execd down %s` and `execd up` again", sock, profile, sock)
		}
		if err := os.RemoveAll(dir); err != nil {
			return "", fmt.Errorf("remove dead session: %w", err)
		}
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("create session dir: %w", err)
	}
	for name, content := range map[string]string{
		"owner": strconv.Itoa(sid), "profile.sha256": sum, "last-used": "",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	logPath := filepath.Join(dir, "log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	cmd := exec.Command(nono, nonoArgs(profile, workdir, self, sock)...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	startErr := cmd.Start()
	logf.Close()
	if startErr != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("start nono: %w", startErr)
	}
	// Reaps the child for as long as this process lives; once the `execd up`
	// process exits, the session is reparented and reaped by init.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		cmd.Process.Kill()
		<-exited
		os.RemoveAll(dir)
		return "", err
	}

	childExited, err := waitForSocketOrExit(sock, startTimeout, exited)
	if err != nil {
		if !childExited {
			cmd.Process.Kill()
			<-exited
		}
		tail := logTail(logPath, 20)
		os.RemoveAll(dir)
		return "", fmt.Errorf("%w\n%s", err, tail)
	}
	return sock, nil
}

// nonoArgs is the `nono run` argv (without "nono") for a session.
// (Carry ExecdArgs's doc comment from internal/claude/launch.go here.)
func nonoArgs(profile, workdir, self, sock string) []string {
	return []string{
		"run", "--silent",
		"--profile", profile,
		"--workdir", workdir,
		"--read-file", self,
		"--allow-unix-socket-bind", sock,
		"--",
		self, "serve", "--socket", sock,
	}
}

// alive reports whether dir's session is running: its pid exists and its
// socket accepts a connection. The probe sends no request, so serve does not
// count it as use.
func alive(dir string) bool {
	pid, err := readInt(filepath.Join(dir, "pid"))
	if err != nil || !processExists(pid) {
		return false
	}
	c, err := net.DialTimeout("unix", filepath.Join(dir, sockName), time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func readInt(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// lockKey serialises up and down for one session directory.
// ponytail: lock files are never removed (one empty file per key ever used);
// removing one while another process waits on it would split the lock.
func lockKey(dir string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, fmt.Errorf("create session root: %w", err)
	}
	f, err := os.OpenFile(dir+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func fileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func logTail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
```

Add below it `waitForSocketOrExit` copied verbatim from `internal/claude/launch.go` (signature `func waitForSocketOrExit(path string, timeout time.Duration, exited <-chan error) (childExited bool, err error)`), changing its final message to `"exec daemon did not start within %s"` (no agent-sandbox hint).

- [ ] **Step 4: Run to verify they pass**

Run: `go test -count=1 ./internal/execd/ -run 'Up_|SessionSocket'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/execd/session.go internal/execd/session_test.go
git commit -m "feat(execd): add Up, which starts or reuses a session

What: execd.Up keys a session by caller session id, workdir and profile,
starts nono under setsid with the session's own directory, and returns the
running session's socket on a repeat call.
Why: execd should own its sandboxed session instead of the launcher."
```

---

### Task 2: `execd.Down`

**Files:**
- Modify: `internal/execd/session.go` (add `Down`, `StopTimeout`, `stop`, `waitGone`)
- Modify: `internal/execd/session_test.go`

**Interfaces:**
- Consumes: `Root`, `alive`, `processExists`, `lockKey`, `readInt`, `sockName` (Task 1).
- Produces: `func Down(sock string) error`; `var StopTimeout = 5 * time.Second`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/execd/session_test.go` (add `"time"` to its imports):

```go
func TestDown_StopsAndRemovesTheSession(t *testing.T) {
	stateHome(t)
	fakeNono(t, passthroughNono)
	sock := upForTest(t, writeProfile(t, "{}"))
	pid, _ := readInt(filepath.Join(filepath.Dir(sock), "pid"))

	if err := Down(sock); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	if processExists(pid) {
		t.Errorf("session process %d still running after Down", pid)
	}
	if _, err := os.Stat(filepath.Dir(sock)); !os.IsNotExist(err) {
		t.Errorf("session dir still exists after Down (stat err %v)", err)
	}
}

func TestDown_KillsAServerThatIgnoresSIGTERM(t *testing.T) {
	stateHome(t)
	fakeNono(t, passthroughNono)
	t.Setenv("EXECD_TEST_WEDGED", "1")
	prev := StopTimeout
	StopTimeout = 200 * time.Millisecond
	t.Cleanup(func() { StopTimeout = prev })
	sock := upForTest(t, writeProfile(t, "{}"))
	pid, _ := readInt(filepath.Join(filepath.Dir(sock), "pid"))

	start := time.Now()
	if err := Down(sock); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("Down() took %v, want it bounded by StopTimeout", d)
	}
	if processExists(pid) {
		t.Errorf("wedged session %d survived Down", pid)
	}
}

// A dead session's pid may belong to an unrelated process by now. The test
// process's own pid stands in for that process: signalling it would end the
// test run.
func TestDown_DeadSessionIsRemovedWithoutSignalling(t *testing.T) {
	stateHome(t)
	sock, err := SessionSocket(writeProfile(t, "{}"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Down(sock); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dead session dir still exists (stat err %v)", err)
	}
}

func TestDown_MissingSessionSucceeds(t *testing.T) {
	stateHome(t)
	sock, _ := SessionSocket(writeProfile(t, "{}"), t.TempDir())
	if err := Down(sock); err != nil {
		t.Errorf("Down() on a missing session error = %v, want nil", err)
	}
}

func TestDown_RefusesAPathOutsideTheRoot(t *testing.T) {
	stateHome(t)
	root, _ := Root()
	victim := t.TempDir()
	for _, sock := range []string{
		filepath.Join(victim, sockName),
		filepath.Join(root, "..", filepath.Base(victim), sockName),
		filepath.Join(root, "abc", "other.sock"),
		filepath.Join(root, "a", "b", sockName),
	} {
		if err := Down(sock); err == nil {
			t.Errorf("Down(%s) = nil, want a refusal", sock)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("a refused Down removed %s: %v", victim, err)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 ./internal/execd/ -run Down_`
Expected: FAIL to compile — `undefined: Down`, `undefined: StopTimeout`.

- [ ] **Step 3: Implement**

Add to `session.go`:

```go
// StopTimeout bounds how long Down waits after SIGTERM before SIGKILL.
// Signal forwarding into a nono session is not execd's to verify, so it cannot
// trust it and wait forever. A var so tests can shrink it.
var StopTimeout = 5 * time.Second

// Down stops the session whose socket is sock and removes its directory. It
// refuses anything but <root>/<key>/execd.sock, because it removes a tree.
// A session already gone is success.
func Down(sock string) error {
	sock, err := filepath.Abs(sock)
	if err != nil {
		return err
	}
	root, err := Root()
	if err != nil {
		return err
	}
	dir := filepath.Dir(sock)
	if filepath.Base(sock) != sockName || filepath.Dir(dir) != root {
		return fmt.Errorf("%s is not an execd session socket under %s", sock, root)
	}
	unlock, err := lockKey(dir)
	if err != nil {
		return err
	}
	defer unlock()
	if _, statErr := os.Stat(dir); errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	// Only a session whose socket still answers is signalled: a dead
	// session's pid may have been reused by an unrelated process. A wedged
	// server still accepts connections at the kernel level.
	if alive(dir) {
		if pid, err := readInt(filepath.Join(dir, "pid")); err == nil {
			stop(pid)
		}
	}
	return os.RemoveAll(dir)
}

func stop(pid int) {
	syscall.Kill(pid, syscall.SIGTERM)
	if waitGone(pid, StopTimeout) {
		return
	}
	syscall.Kill(pid, syscall.SIGKILL)
	waitGone(pid, time.Second)
}

// waitGone polls rather than waits: the session is not this process's child
// once `execd up` has exited.
func waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return !processExists(pid)
}
```

- [ ] **Step 4: Run to verify they pass**

Run: `go test -count=1 ./internal/execd/`
Expected: PASS (whole package).

- [ ] **Step 5: Commit**

```bash
git add internal/execd/session.go internal/execd/session_test.go
git commit -m "feat(execd): add Down, which stops and removes a session

What: execd.Down SIGTERMs a live session, escalates to SIGKILL after
StopTimeout, removes its directory, and refuses paths outside the root.
Why: stopping belongs with starting, in execd."
```

---

### Task 3: `serve` records use

**Files:**
- Modify: `internal/execd/server.go` (`handle`, new `touchLastUsed`)
- Modify: `internal/execd/server_test.go`

**Interfaces:**
- Produces: a request served on `<dir>/execd.sock` updates the mtime of an existing `<dir>/last-used`; nothing is created.

- [ ] **Step 1: Write the failing tests**

Append to `internal/execd/server_test.go` (package `execd_test`; add imports `"net"` and `"time"` if missing):

```go
// lastUsedBeside creates last-used next to sock, dated a week ago.
func lastUsedBeside(t *testing.T, sock string) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(sock), "last-used")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-7 * 24 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestServe_ARequestUpdatesLastUsed(t *testing.T) {
	sock := startTestServer(t, execd.NewShellExecutor())
	p := lastUsedBeside(t, sock)
	out, _ := outFile(t)
	errFile, _ := outFile(t)
	if _, err := execd.NewClient(sock).RunCommand(context.Background(), "true",
		execd.Stdio{In: devNull(t), Out: out, Err: errFile}, execd.RunOptions{}); err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(info.ModTime()) > time.Minute {
		t.Errorf("last-used mtime = %v, want it updated by the request", info.ModTime())
	}
}

func TestServe_AProbeConnectionDoesNotCountAsUse(t *testing.T) {
	sock := startTestServer(t, execd.NewShellExecutor())
	p := lastUsedBeside(t, sock)
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	time.Sleep(100 * time.Millisecond)
	info, _ := os.Stat(p)
	if time.Since(info.ModTime()) < time.Hour {
		t.Errorf("a connection without a request updated last-used to %v", info.ModTime())
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 ./internal/execd/ -run Serve_`
Expected: `TestServe_ARequestUpdatesLastUsed` FAILS ("want it updated by the request"); the probe test passes already (it guards the placement in Step 3).

- [ ] **Step 3: Implement**

In `internal/execd/server.go`, directly after the successful `req, err := ReadRequest(conn)` block in `handle`, add `s.touchLastUsed()`, and add the method (import `path/filepath` and `time` if missing):

```go
// touchLastUsed records a served request for `execd gc`'s idle judgement. It
// runs only after a request has been read, so a liveness probe that connects
// and hangs up is not use. It only updates an existing file: a server outside
// an execd session directory creates nothing beside its socket. A failure is
// ignored; it only affects gc.
func (s *Server) touchLastUsed() {
	now := time.Now()
	os.Chtimes(filepath.Join(filepath.Dir(s.sockPath), "last-used"), now, now)
}
```

- [ ] **Step 4: Run to verify they pass**

Run: `go test -count=1 ./internal/execd/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/execd/server.go internal/execd/server_test.go
git commit -m "feat(execd): record each served request in last-used

What: serve updates the mtime of last-used beside its socket after reading
a request; liveness probes do not count.
Why: execd gc needs to know which sessions have gone unused."
```

---

### Task 4: `execd.GC`

**Files:**
- Modify: `internal/execd/session.go`
- Modify: `internal/execd/session_test.go`

**Interfaces:**
- Consumes: `Root`, `alive`, `readInt`, `Down`, `sockName`.
- Produces: `func GC(idle time.Duration, dryRun bool, w io.Writer) error`. Output: one line per session, `<action> <socket> (<reason>)`, action ∈ `remove`, `down`, `keep`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/execd/session_test.go` (add `"bytes"` to imports):

```go
// gcFixture builds four sessions: dead, owner gone, idle, and fresh.
func gcFixture(t *testing.T) (dead, ownerGone, idle, fresh string) {
	t.Helper()
	stateHome(t)
	fakeNono(t, passthroughNono)
	profile := writeProfile(t, "{}")

	withSession(t, 2001)
	dead, _ = SessionSocket(profile, t.TempDir())
	if err := os.MkdirAll(filepath.Dir(dead), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(filepath.Dir(dead), "pid"), []byte(strconv.Itoa(deadPID(t))), 0o600)

	withSession(t, 2002)
	ownerGone = upForTest(t, profile)
	os.WriteFile(filepath.Join(filepath.Dir(ownerGone), "owner"), []byte(strconv.Itoa(deadPID(t))), 0o600)

	withSession(t, os.Getpid()) // an owner that exists
	idle = upForTest(t, profile)
	old := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(filepath.Join(filepath.Dir(idle), "last-used"), old, old)

	withSession(t, os.Getpid())
	fresh, _ = Up(profile, t.TempDir())
	t.Cleanup(func() { killSession(fresh) })
	return dead, ownerGone, idle, fresh
}

func TestGC_DownsASessionWhoseOwnerIsGone(t *testing.T) {
	dead, ownerGone, idle, fresh := gcFixture(t)
	var out bytes.Buffer
	if err := GC(168*time.Hour, false, &out); err != nil {
		t.Fatalf("GC() error = %v\n%s", err, out.String())
	}
	for _, gone := range []string{dead, ownerGone, idle} {
		if _, err := os.Stat(filepath.Dir(gone)); !os.IsNotExist(err) {
			t.Errorf("GC left %s (stat err %v)\n%s", gone, err, out.String())
		}
	}
	if !alive(filepath.Dir(fresh)) {
		t.Errorf("GC stopped the fresh session %s\n%s", fresh, out.String())
	}
}

func TestGC_DryRunChangesNothing(t *testing.T) {
	dead, ownerGone, idle, fresh := gcFixture(t)
	var out bytes.Buffer
	if err := GC(168*time.Hour, true, &out); err != nil {
		t.Fatalf("GC(dry run) error = %v", err)
	}
	for _, s := range []string{dead, ownerGone, idle, fresh} {
		if _, err := os.Stat(filepath.Dir(s)); err != nil {
			t.Errorf("dry run removed %s: %v", s, err)
		}
		if !strings.Contains(out.String(), s) {
			t.Errorf("dry run output does not list %s:\n%s", s, out.String())
		}
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test -count=1 ./internal/execd/ -run GC_`
Expected: FAIL to compile — `undefined: GC`.

- [ ] **Step 3: Implement**

Append to `session.go` (add `"io"` to its imports):

```go
// GC cleans up every session under the root: a dead one is removed, one
// whose owner shell is gone or whose last-used is older than idle is downed,
// anything else is kept. dryRun only reports. One session failing does not
// stop the rest.
func GC(idle time.Duration, dryRun bool, w io.Writer) error {
	root, err := Root()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	failed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		sock := filepath.Join(dir, sockName)
		action, reason := judge(dir, idle)
		fmt.Fprintf(w, "%s %s (%s)\n", action, sock, reason)
		if dryRun || action == "keep" {
			continue
		}
		// Down rechecks liveness under the key's lock, so a session that
		// changed state since judge is still handled safely.
		if err := Down(sock); err != nil {
			fmt.Fprintf(w, "  failed: %v\n", err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d session(s) could not be cleaned up", failed)
	}
	return nil
}

func judge(dir string, idle time.Duration) (action, reason string) {
	if !alive(dir) {
		return "remove", "not running"
	}
	if owner, err := readInt(filepath.Join(dir, "owner")); err == nil &&
		errors.Is(syscall.Kill(owner, 0), syscall.ESRCH) {
		return "down", "its shell is gone"
	}
	if info, err := os.Stat(filepath.Join(dir, "last-used")); err == nil && time.Since(info.ModTime()) > idle {
		return "down", "unused since " + info.ModTime().Format(time.RFC3339)
	}
	return "keep", "in use"
}
```

- [ ] **Step 4: Run to verify they pass**

Run: `go test -count=1 ./internal/execd/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/execd/session.go internal/execd/session_test.go
git commit -m "feat(execd): add GC for dead, orphaned and idle sessions

What: execd.GC removes dead sessions and downs those whose shell is gone or
that have gone unused longer than idle; dry run only reports.
Why: a launcher killed before cleanup leaves its session running."
```

---

### Task 5: `execd up`, `down`, `gc` subcommands

**Files:**
- Modify: `cmd/execd/main.go`
- Modify: `cmd/execd/main_test.go`

**Interfaces:**
- Consumes: `execd.Up`, `execd.Down`, `execd.GC` (Tasks 1, 2, 4).
- Produces: CLI `execd up --profile <p> [--workdir <d>]`, `execd down <socket>`, `execd gc [--idle d] [--dry-run]`.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/execd/main_test.go`:

```go
func TestUp_RequiresProfile(t *testing.T) {
	if code := run([]string{"up"}); code != 2 {
		t.Errorf("run(up) = %d, want 2 without --profile", code)
	}
}

func TestDown_RequiresExactlyOneSocket(t *testing.T) {
	for _, args := range [][]string{{"down"}, {"down", "a", "b"}} {
		if code := run(args); code != 2 {
			t.Errorf("run(%v) = %d, want 2", args, code)
		}
	}
}

func TestDown_RefusalExits1(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if code := run([]string{"down", "/tmp/not-a-session/execd.sock"}); code != 1 {
		t.Errorf("run(down outside root) = %d, want 1", code)
	}
}

func TestGC_BadIdleIsUsage(t *testing.T) {
	if code := run([]string{"gc", "--idle", "soon"}); code != 2 {
		t.Errorf("run(gc --idle soon) = %d, want 2", code)
	}
}

func TestGC_EmptyRootSucceeds(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if code := run([]string{"gc", "--dry-run"}); code != 0 {
		t.Errorf("run(gc --dry-run) on an empty root = %d, want 0", code)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./cmd/execd/`
Expected: FAIL — these subcommands fall through to usage (exit 2), so `TestDown_RefusalExits1` and `TestGC_EmptyRootSucceeds` fail.

- [ ] **Step 3: Implement**

In `cmd/execd/main.go`: extend the package doc comment and `usage` with the three commands; add cases to `run`; add the handlers (import `"time"`):

```go
const usage = `usage:
  execd up --profile <path> [--workdir <dir>]
  execd down <socket>
  execd gc [--idle <duration>] [--dry-run]
  execd serve --socket <path>
  execd exec [--timeout d] -- <command>`
```

```go
	case "up":
		return up(args[1:])
	case "down":
		return down(args[1:])
	case "gc":
		return gc(args[1:])
```

```go
func up(args []string) int {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	profile := fs.String("profile", "", "nono profile the session runs under (required)")
	workdir := fs.String("workdir", "", "directory $WORKDIR expands to (default: current directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *profile == "" || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "execd up: --profile is required and takes no arguments")
		return 2
	}
	sock, err := execd.Up(*profile, *workdir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "execd up: %v\n", err)
		return 1
	}
	fmt.Println(sock)
	return 0
}

func down(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "execd down: takes exactly one socket path")
		return 2
	}
	if err := execd.Down(args[0]); err != nil {
		fmt.Fprintf(os.Stderr, "execd down: %v\n", err)
		return 1
	}
	return 0
}

func gc(args []string) int {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	idle := fs.Duration("idle", 168*time.Hour, "down sessions unused for longer than this")
	dryRun := fs.Bool("dry-run", false, "only report what would be cleaned up")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := execd.GC(*idle, *dryRun, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "execd gc: %v\n", err)
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Run to verify they pass**

Run: `go test -count=1 ./cmd/execd/ ./internal/execd/ && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/execd
git commit -m "feat(execd): add the up, down and gc subcommands"
```

---

### Task 6: The launcher calls `execd up` / `execd down`

**Files:**
- Modify: `internal/claude/launch.go` (`startExecd`; delete `ExecdArgs`, `ExecdSocketPath`, `waitForSocketOrExit`, `execdStartTimeout`, `execdStopTimeout`)
- Modify: `internal/claude/launch_test.go`
- Modify: `internal/claude/execd_e2e_test.go`
- Modify: `internal/cli/debug.go`, `internal/cli/debug_test.go`
- Modify: `internal/cli/doctor.go` (`checkExecdSocketDir`), `internal/cli/doctor_test.go`

**Interfaces:**
- Consumes: CLI `execd up` / `execd down` (Task 5); `execd.SessionSocket`, `execd.Root` (Task 1).
- Produces: `startExecd(cfg *config.Config) (socket, execdPath string, cleanup func(), err error)` — unchanged signature.

- [ ] **Step 1: Rework the launcher tests**

In `internal/claude/launch_test.go`:

a. In `makeFakeNono`, replace the fake `execd` (`#!/bin/sh\nexit 0\n`) with one that answers `up` and records `down`:

```go
	execdScript := "#!/bin/sh\nd=$(dirname \"$0\")\n" +
		"case \"$1\" in\n" +
		"up) echo \"$d/fake.sock\" ;;\n" +
		"down) echo \"$2\" > \"$d/down-arg\" ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "execd"), []byte(execdScript), 0755); err != nil {
		t.Fatal(err)
	}
```

b. Delete these tests, whose subject moved to `internal/execd`: `TestStartExecd_ReportsChildExitBeforeBinding`, `TestStartExecd_CleanupKillsAWedgedExecd`, `TestStartExecd_CreatesAndRemovesTheSocketDirectory`, `TestExecdArgsRunsExecdUnderTheCommandProfile`, `TestExecdArgsGrantsItsOwnBinaryAndInvokesItAbsolutely`, `TestExecdArgsUsesTheResolvedNonoPath`, the four `TestWaitForSocketOrExit_*`, and `makeFakeNonoWedged` once nothing uses it.

c. Replace `TestExecdSocketPath_HasADirectoryOfItsOwn` with:

```go
// The session socket's bind grant reaches its parent directory, so that
// directory must not be the one holding the agent's shell wrapper.
func TestExecdSessionSocket_IsNotBesideTheShellWrapper(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	sock, err := execd.SessionSocket("/p/command-profile.json", "/w")
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := ShellWrapperPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(sock) == filepath.Dir(wrapper) || filepath.Dir(filepath.Dir(sock)) == filepath.Dir(wrapper) {
		t.Errorf("socket %s sits in or beside the shell wrapper's directory %s", sock, filepath.Dir(wrapper))
	}
}
```

d. Add:

```go
func TestStartExecd_RunsUpAndDown(t *testing.T) {
	nono := makeFakeNono(t)
	dir := filepath.Dir(nono)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	sock, execdPath, cleanup, err := startExecd(&config.Config{})
	if err != nil {
		t.Fatalf("startExecd() error = %v", err)
	}
	if want := filepath.Join(dir, "fake.sock"); sock != want {
		t.Errorf("socket = %q, want what execd up printed, %q", sock, want)
	}
	if execdPath != filepath.Join(dir, "execd") {
		t.Errorf("execd path = %q", execdPath)
	}
	cleanup()
	got, err := os.ReadFile(filepath.Join(dir, "down-arg"))
	if err != nil || strings.TrimSpace(string(got)) != sock {
		t.Errorf("execd down got %q (err %v), want %q", got, err, sock)
	}
}

func TestStartExecd_UpFailureFailsTheLaunch(t *testing.T) {
	nono := makeFakeNono(t)
	failing := "#!/bin/sh\necho 'profile rejected' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(nono), "execd"), []byte(failing), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, _, cleanup, err := startExecd(&config.Config{})
	if err == nil || !strings.Contains(err.Error(), "execd up") {
		t.Fatalf("startExecd() error = %v, want an execd up failure", err)
	}
	if cleanup != nil {
		t.Error("cleanup should be nil on error")
	}
}
```

Add the import `"github.com/ynny-github/agent-sandbox/internal/execd"` to `launch_test.go` if it is not there.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/claude/ -run 'StartExecd|SessionSocket'`
Expected: FAIL — `TestStartExecd_RunsUpAndDown` gets a socket under the state dir instead of `fake.sock`; `down-arg` missing.

- [ ] **Step 3: Implement in `internal/claude/launch.go`**

In `startExecd`, keep everything up to and including the writability loop and `cwd`. Replace the rest (from `sockPath, err := ExecdSocketPath()` to the end) with:

```go
	// execd owns the session: where the socket lives, the nono argv, waiting
	// for it, and stopping it. The launcher only decided which execd to trust.
	up := exec.Command(execdPath, "up", "--profile", cfg.CommandProfilePath(), "--workdir", cwd)
	up.Stderr = os.Stderr
	out, err := up.Output()
	if err != nil {
		return "", "", nil, fmt.Errorf("execd up: %w", err)
	}
	sock := strings.TrimSpace(string(out))
	cleanup := func() {
		down := exec.Command(execdPath, "down", sock)
		down.Stdout, down.Stderr = os.Stderr, os.Stderr
		if err := down.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "agent-sandbox: execd down: %v\n", err)
		}
	}
	return sock, execdPath, cleanup, nil
```

The `nono` lookup at the top of `startExecd` stays: the writability check runs `nono why`. Delete `execdStartTimeout`, `execdStopTimeout`, `waitForSocketOrExit`, `ExecdArgs` and `ExecdSocketPath` with their comments. Update `startExecd`'s doc comment: it resolves and checks execd, then runs `execd up` and returns a cleanup that runs `execd down`. Remove imports that become unused (`syscall`, `time`, `policysnapshot` if unused — `go build` names them).

- [ ] **Step 4: Update debug**

In `internal/cli/debug.go`, replace `execdSocket, err := claude.ExecdSocketPath()` with:

```go
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	execdSocket, err := execd.SessionSocket(cfg.CommandProfilePath(), cwd)
	if err != nil {
		return err
	}
```

and replace the exec-daemon block (the `claude.ExecdArgs(...)` line and the later `cwd` lookup) with:

```go
	if execdErr != nil {
		execdPath = "execd"
	}
	fmt.Fprintln(cmd.OutOrStdout(), "exec daemon:")
	fmt.Fprintln(cmd.OutOrStdout(), "  "+strings.Join([]string{
		execdPath, "up", "--profile", cfg.CommandProfilePath(), "--workdir", cwd}, " "))
	if execdErr != nil {
		fmt.Fprintln(cmd.OutOrStdout(), "  ("+execdErr.Error()+")")
	}
```

Delete `nonoPathForDisplay` if nothing else uses it. In `internal/cli/debug_test.go`: `TestRunDebug_PrintsExecdSocketGrant` computes `wantSocket` with `execd.SessionSocket(cfgCommandProfile, cwd)` where `cfgCommandProfile = filepath.Join(dir, "command-profile.json")` and `cwd, _ := os.Getwd()`; replace `TestRunDebug_PrintsResolvedNonoPath` with `TestRunDebug_PrintsTheExecdUpLine`, asserting the output contains `"exec daemon:\n  execd up --profile " + filepath.Join(dir, "command-profile.json")` (execd is not on the test PATH, so the literal `execd` is printed).

- [ ] **Step 5: Update doctor**

In `checkExecdSocketDir` (`internal/cli/doctor.go`), replace `policysnapshot.StateDir()` with `execd.Root()`, and update its doc comment ("where execd up creates sessions"). In `internal/cli/doctor_test.go` (tests around lines 298-350), change the expected directory to `filepath.Join(<the test's XDG_STATE_HOME>, "execd")`. Remove the `policysnapshot` import from doctor.go if now unused.

- [ ] **Step 6: Rewrite the e2e session helper**

In `internal/claude/execd_e2e_test.go`, replace `startExecdSession` with:

```go
// startExecdSession runs the real `execd up` against a real nono and returns
// the socket; `execd down` is registered for cleanup. XDG_STATE_HOME is a
// short temp dir: socket paths are capped near 104 bytes.
func startExecdSession(t *testing.T, execdPath, profile, workdir string) string {
	t.Helper()
	state, err := os.MkdirTemp("", "xde2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", state)
	up := exec.Command(execdPath, "up", "--profile", profile, "--workdir", workdir)
	var errBuf strings.Builder
	up.Stderr = &errBuf
	out, err := up.Output()
	if err != nil {
		t.Fatalf("execd up: %v\n%s", err, errBuf.String())
	}
	sock := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		exec.Command(execdPath, "down", sock).Run()
		os.RemoveAll(state)
	})
	return sock
}
```

Update its caller to `startExecdSession(t, execdPath, cfg.CommandProfile, workdir)` (drop the `nonoPath` argument; keep the `nono` LookPath skip). Add:

```go
func TestExecdUp_TwiceReturnsTheSamePath(t *testing.T) {
	if _, err := exec.LookPath("nono"); err != nil {
		t.Skip("nono not on PATH")
	}
	workdir := t.TempDir()
	execdPath := buildExecd(t, workdir)
	profile := writeFixtureProfile(t, workdir, nil)
	first := startExecdSession(t, execdPath, profile, workdir)
	out, err := exec.Command(execdPath, "up", "--profile", profile, "--workdir", workdir).Output()
	if err != nil {
		t.Fatalf("second execd up: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != first {
		t.Errorf("second execd up = %s, want %s", got, first)
	}
}
```

Check `writeFixtureProfile`'s actual signature in the file and match it (the existing callers show the argument list).

- [ ] **Step 7: Run everything**

Run: `go build ./... && go vet ./... && go vet -tags e2e ./... && go test -count=1 ./...`
Expected: PASS.
Run: `go test -count=1 -tags e2e ./internal/claude/`
Expected: PASS with nono installed (not skipped — check with `-v | grep -c -- '--- PASS'`).

- [ ] **Step 8: Commit**

```bash
git add -A internal
git commit -m "feat(claude): start and stop execd through execd up and down

What: startExecd resolves and checks execd as before, then runs execd up and
returns a cleanup that runs execd down; the socket, nono argv, start wait and
stop escalation leave launch.go. debug prints the execd up line; doctor
probes execd's own session root.
Why: execd owns its session; the launcher only decides which execd to trust."
```

---

### Task 7: Docs

**Files:**
- Modify: `README.md`, `README.ja.md`

- [ ] **Step 1: README.md**

- In the execd command table, add rows above `execd serve`:

```markdown
| `execd up --profile <path> [--workdir <dir>]` | Start the session for this shell, workdir and profile under nono, or reuse the running one; print its socket. `agent-sandbox claude` runs this |
| `execd down <socket>` | Stop that session and remove its directory. `agent-sandbox claude` runs this on exit |
| `execd gc [--idle <duration>] [--dry-run]` | Clean up sessions that are dead, whose shell is gone, or unused for longer than `--idle` (default 168h) |
```

- Change the `execd serve` row's description to: "Serve commands on the socket. `execd up` runs it inside the command-profile session; never run it unsandboxed, it executes whatever it is sent".
- After the table add: "Sessions live under `$XDG_STATE_HOME/execd/` (`~/.local/state/execd/`). One that `agent-sandbox claude` could not stop — the launcher was killed — keeps running until `execd gc` collects it."

- [ ] **Step 2: README.ja.md**

Mirror Step 1:

```markdown
| `execd up --profile <path> [--workdir <dir>]` | このシェル・workdir・プロファイルのセッションを nono の下で起動する(動いていればそれを使う)。ソケットを出力する。`agent-sandbox claude` が実行する |
| `execd down <socket>` | そのセッションを止めてディレクトリを消す。`agent-sandbox claude` が終了時に実行する |
| `execd gc [--idle <duration>] [--dry-run]` | 死んでいる・シェルが消えた・`--idle`(既定 168h)より長く使われていないセッションを片付ける |
```

`execd serve` row: 「ソケットでコマンドを受け付ける。`execd up` がコマンドプロファイルのセッション内で起動する。受け取ったものをそのまま実行するので、sandbox の外では動かさないこと」. After the table: 「セッションは `$XDG_STATE_HOME/execd/`(`~/.local/state/execd/`)に置かれます。ランチャーが kill されるなどで `agent-sandbox claude` が止められなかったセッションは、`execd gc` が片付けるまで動き続けます。」

- [ ] **Step 3: Verify and commit**

Run: `grep -n "execd up\|execd gc" README.md README.ja.md | wc -l`
Expected: at least 6.

```bash
git add README.md README.ja.md
git commit -m "docs: document execd up, down and gc"
```
