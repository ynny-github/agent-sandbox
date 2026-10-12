package execd

import (
	"bytes"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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
