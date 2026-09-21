package execd_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/execd"
)

// TestWiringDoesNotLeakARedirectToTheCallersWriter checks what this case can
// actually check: the redirect lands in the file and none of it also reaches the
// caller's own stdout. It does not distinguish a file passed straight to the child
// from one a drain copies into — that check is
// TestWiringGivesTheChildTheRedirectsOwnFile.
func TestWiringDoesNotLeakARedirectToTheCallersWriter(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	e := execd.NewShellExecutor()
	passed, readPassed := outFile(t)
	code, err := e.Run(context.Background(), "echo redirected > out.txt", dir,
		execd.Stdio{In: devNull(t), Out: passed, Err: nullOut(t)})
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	b, rerr := os.ReadFile(out)
	if rerr != nil || strings.TrimSpace(string(b)) != "redirected" {
		t.Fatalf("file = %q, %v; want \"redirected\"", b, rerr)
	}
	if readPassed() != "" {
		t.Errorf("redirected output also reached the caller's own stdout: %q", readPassed())
	}
}

func TestWiringKeepsPipelinesTerminating(t *testing.T) {
	e := execd.NewShellExecutor()
	out, readOut := outFile(t)
	done := make(chan int, 1)
	go func() {
		code, _ := e.Run(context.Background(), "seq 1 100000 | head -n 3", t.TempDir(),
			execd.Stdio{In: devNull(t), Out: out, Err: nullOut(t)})
		done <- code
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("pipeline did not terminate")
	}
	if got := strings.Fields(readOut()); len(got) != 3 {
		t.Errorf("output = %q, want three lines", readOut())
	}
}

func TestWiringMergesWhenStdoutAndStderrAreOneWriter(t *testing.T) {
	e := execd.NewShellExecutor()
	both, readBoth := outFile(t)
	if _, err := e.Run(context.Background(), "sh -c 'echo o; echo e >&2' 2>&1",
		t.TempDir(), execd.Stdio{In: devNull(t), Out: both, Err: both}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readBoth(), "o") || !strings.Contains(readBoth(), "e") {
		t.Errorf("merged output = %q, want both streams", readBoth())
	}
}

// TestWiringGivesTheChildTheRedirectsOwnFile checks the claim
// TestWiringDoesNotLeakARedirectToTheCallersWriter cannot: the bytes land in
// the file either way — a drain copies them there just as faithfully as the
// child writing them — so the only observable difference is what the child's
// own fd 1 actually is. `[ -f /dev/stdout ]` is true only when it is the regular file
// the shell opened, and false when it is a pipe this side interposed. sh, not
// the interpreter's echo builtin, so the command really reaches execHandler.
func TestWiringGivesTheChildTheRedirectsOwnFile(t *testing.T) {
	dir := t.TempDir()
	code, _, errb := runShell(t, dir,
		"sh -c '[ -f /dev/stdout ] && echo file || echo pipe' > kind.txt", "")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errb)
	}
	b, err := os.ReadFile(filepath.Join(dir, "kind.txt"))
	if err != nil {
		t.Fatalf("read kind.txt: %v", err)
	}
	if got := strings.TrimSpace(string(b)); got != "file" {
		t.Errorf("child's stdout was a %s; a redirect's own file must reach the child unwrapped "+
			"(this check needs a procfs-style /dev/stdout that resolves to the child's own fd 1; "+
			"where /dev/stdout is not that, it reports %q whatever the wiring did)", got, "pipe")
	}
}

// TestWiringGivesTheChildTheRedirectsOwnStdinFile is the same claim for the
// input side: `cmd < f.txt` hands the child the file the shell opened, with no
// pipe and no copy goroutine between them.
func TestWiringGivesTheChildTheRedirectsOwnStdinFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "in.txt"), []byte("ignored\n"), 0o600); err != nil {
		t.Fatalf("write in.txt: %v", err)
	}
	code, out, errb := runShell(t, dir,
		"sh -c '[ -f /dev/stdin ] && echo file || echo pipe' < in.txt", "")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errb)
	}
	if got := strings.TrimSpace(out); got != "file" {
		t.Errorf("child's stdin was a %s; a redirect's own file must reach the child unwrapped "+
			"(this check needs a procfs-style /dev/stdin that resolves to the child's own fd 0; "+
			"where /dev/stdin is not that, it reports %q whatever the wiring did)", got, "pipe")
	}
}

// TestWiringInterposesAPipedStdin is a regression smoke test for the other half
// of the stdin rule, not a check of it: `cat` reproduces the caller's stdin
// whether that stdin was interposed or handed over directly, so what this pins
// is only that a non-file stdin still arrives intact. The interposition itself
// is observable only with nono's shim holding a duplicate of the fd — without
// the shim, a child that receives the interpreter's own pipe read end closes it
// on exit and nothing goes wrong — so no test in this package can distinguish
// the two.
func TestWiringInterposesAPipedStdin(t *testing.T) {
	dir := t.TempDir()
	code, out, errb := runShell(t, dir, "cat", "from the caller")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errb)
	}
	if strings.TrimSpace(out) != "from the caller" {
		t.Errorf("stdout = %q, want %q", out, "from the caller")
	}
}

// TestWiringGivesTheChildThePassedOutputDescriptor is the end-to-end half of
// the claim Task 3's TestWiringPassesAnInheritedPipeToTheChild makes about
// passthrough alone: a file the request was given reaches the child as itself,
// not as a pipe this side interposed and copies out of.
//
// Output only, as the name says. The stdin half of the rule is not covered
// here and cannot be covered the same way: a child reading from an interposed
// pipe receives exactly the bytes the passed descriptor would have given it,
// and the input side has no analogue of the probe below — a pipe with no
// writer left reads as EOF, which is also what an interposed pipe reads as
// once its copier is done, so there is nothing for a child to report that
// differs. What the stdin rule actually buys shows up only against nono's
// shim, which retains a duplicate of every descriptor it is handed; no test in
// this package can hold a shim. wireStdin's comment carries that reasoning,
// and TestWiringInterposesAPipedStdin is explicitly a smoke test rather than a
// check of the rule for the same reason.
//
// Even on the output side it takes an awkward probe, because nothing direct
// can see the difference: the bytes land in the caller's pipe either way, a
// drain copying them there just as faithfully as the child writing them.
//
// So the probe is a pipe whose read end is already closed. A child that holds
// the caller's own write end writes into it and dies of SIGPIPE, reported as
// 128+SIGPIPE. A child handed an interposed pipe writes into that successfully
// and exits 0; the EPIPE lands on this side's drain instead, where it is
// silent. Measured both ways, 5 runs each: 141 with identity in the wiring, 0
// with it removed.
//
// sh, not the interpreter's echo builtin, so the command really reaches
// execHandler — a builtin writes through the interpreter and never asks the
// wiring anything.
func TestWiringGivesTheChildThePassedOutputDescriptor(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pw.Close() })
	// Closed before the command runs, so the very first write the child makes
	// is the one that answers the question.
	if err := pr.Close(); err != nil {
		t.Fatal(err)
	}

	errf, readErr := outFile(t)
	code, err := execd.NewShellExecutor().Run(context.Background(),
		"sh -c 'echo probe'", t.TempDir(),
		execd.Stdio{In: devNull(t), Out: pw, Err: errf})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := 128 + int(syscall.SIGPIPE); code != want {
		t.Errorf("exit = %d, want %d (128+SIGPIPE); the child wrote somewhere other than "+
			"the caller's own descriptor, so the wiring interposed a pipe instead of "+
			"recognising the request's own file (stderr %q)", code, want, readErr())
	}
}
