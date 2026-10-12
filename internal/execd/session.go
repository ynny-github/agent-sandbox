package execd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// sockName is the socket's file name; sockSubdir is the directory it sits in,
// inside the session directory.
//
// The socket gets a directory of its own because its bind grant makes that
// whole directory writable to the sandbox. The files up, down and gc trust
// from outside it (pid, owner, profile.sha256, log) therefore live one level
// up: beside the socket, a command could write another process's pid into
// `pid` and have the next `execd down` SIGKILL it. Only last-used, which serve
// itself writes and which only steers gc's idle judgement, sits beside it.
const (
	sockName   = "execd.sock"
	sockSubdir = "sock"
)

// startTimeout bounds how long Up waits for the socket. A sandbox that
// cannot start fails here rather than leaving its caller waiting on an execd
// that will never answer.
const startTimeout = 15 * time.Second

// callerSession is getsid(0): the session of the process running Up, which is
// the interactive shell's pid for everything started from that shell. A var so
// tests can stand in two shells.
var callerSession = func() (int, error) { return unix.Getsid(0) }

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
	return filepath.Join(dir, sockSubdir, sockName), nil
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

	sock := filepath.Join(dir, sockSubdir, sockName)
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
	if err := os.Mkdir(filepath.Join(dir, sockSubdir), 0o700); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("create socket dir: %w", err)
	}
	for name, content := range map[string]string{
		"owner": strconv.Itoa(sid), "profile.sha256": sum,
		filepath.Join(sockSubdir, "last-used"): "",
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
//
// execd is a sibling of the agent's sandbox, not a child of it: nono
// refuses to nest, and execd must be the session entrypoint so the profile
// applies to everything it executes. It is deliberately not given --allow-cwd;
// the working directory reaches the profile through --workdir, which is what
// $WORKDIR expands to inside it.
//
// Everything execd needs in order to *start* is granted here, on the
// command line, rather than being left to the operator's profile. --read-file
// covers the execd binary (a read grant carries the execute right), and
// --allow-unix-socket-bind covers the socket. Measured: under a profile that
// grants neither, an absolute-path invocation of that binary exits 127 with no
// output; adding --read-file alone makes the same invocation run. Keeping these
// on execd's side means an operator narrowing what *commands* may reach
// cannot accidentally stop execd from starting, and it removes the
// profile's grants from the set of things that decide whether a session comes
// up at all.
//
// The entrypoint is invoked by its absolute path. It was invoked by base name
// while the profile carried command_policies, because tool-sandbox treats an
// absolute-path invocation of a policy-controlled command as a direct exec
// bypass and refuses it. With no command_policies left, that constraint is gone
// and only base-name resolution's own hazard would remain: nono resolves the
// bare name through the caller's PATH, so a different, stale copy earlier on
// PATH would silently become execd instead of this one.
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
	c, err := net.DialTimeout("unix", filepath.Join(dir, sockSubdir, sockName), time.Second)
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

// waitForSocketOrExit waits for execd's socket to appear, racing that
// against the child exiting first via exited. Polling for the socket (rather
// than a readiness handshake) is necessary because execd is behind a
// sandbox boundary: there is no shared channel to signal on until the socket
// itself appears. Racing it against exited is what turns "nono rejected the
// profile and the child exited within milliseconds" into an immediate,
// specific error instead of a silent wait for the full timeout.
//
// childExited reports whether exited fired (in which case the child is
// already reaped and err explains why it died) or the deadline passed with
// the child still running (in which case the caller still owns stopping and
// reaping it).
func waitForSocketOrExit(path string, timeout time.Duration, exited <-chan error) (childExited bool, err error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(path); statErr == nil {
			return false, nil
		}
		select {
		case waitErr := <-exited:
			if waitErr == nil {
				// A nil error from cmd.Wait means the process exited 0: unusual for
				// nono to do without ever binding the socket, but still reported as
				// a specific, immediate failure rather than folded into the
				// generic timeout message below.
				return true, fmt.Errorf("exec daemon exited (status 0) before binding its socket")
			}
			return true, fmt.Errorf("exec daemon exited before binding its socket: %w", waitErr)
		case <-time.After(25 * time.Millisecond):
		}
	}
	return false, fmt.Errorf("exec daemon did not start within %s", timeout)
}

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
	dir := sessionOf(sock)
	if filepath.Base(sock) != sockName || filepath.Base(filepath.Dir(sock)) != sockSubdir ||
		filepath.Dir(dir) != root {
		return fmt.Errorf("%s is not an execd session socket under %s", sock, root)
	}
	unlock, err := lockKey(dir)
	if err != nil {
		return err
	}
	defer unlock()
	return downLocked(dir)
}

// downLocked stops dir's session and removes it; the caller holds its lock.
func downLocked(dir string) error {
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
		if err := gcOne(dir, idle, dryRun, w); err != nil {
			fmt.Fprintf(w, "  failed: %v\n", err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d session(s) could not be cleaned up", failed)
	}
	return nil
}

// gcOne judges and acts on one session under its key's lock, so an up in
// progress is never judged half-started and then stopped once it finishes.
func gcOne(dir string, idle time.Duration, dryRun bool, w io.Writer) error {
	unlock, err := lockKey(dir)
	if err != nil {
		return err
	}
	defer unlock()
	action, reason := judge(dir, idle)
	fmt.Fprintf(w, "%s %s (%s)\n", action, filepath.Join(dir, sockSubdir, sockName), reason)
	if dryRun || action == "keep" {
		return nil
	}
	return downLocked(dir)
}

func judge(dir string, idle time.Duration) (action, reason string) {
	if !alive(dir) {
		return "remove", "not running"
	}
	if owner, err := readInt(filepath.Join(dir, "owner")); err == nil &&
		errors.Is(syscall.Kill(owner, 0), syscall.ESRCH) {
		return "down", "its shell is gone"
	}
	if info, err := os.Stat(filepath.Join(dir, sockSubdir, "last-used")); err == nil && time.Since(info.ModTime()) > idle {
		return "down", "unused since " + info.ModTime().Format(time.RFC3339)
	}
	return "keep", "in use"
}

// sessionOf is the session directory a socket path belongs to.
func sessionOf(sock string) string { return filepath.Dir(filepath.Dir(sock)) }
