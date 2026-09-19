package execd_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/execd"
)

// TestWiringDoesNotLeakARedirectToTheCallersWriter checks what this case can
// actually check: the redirect lands in the file and none of it also reaches the
// caller's writer. It does not distinguish a file passed straight to the child
// from one a drain copies into — that check is
// TestWiringGivesTheChildTheRedirectsOwnFile.
func TestWiringDoesNotLeakARedirectToTheCallersWriter(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	e := execd.NewShellExecutor()
	var framed strings.Builder
	code, err := e.Run(context.Background(), "echo redirected > out.txt", dir, nil, &framed, io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	b, rerr := os.ReadFile(out)
	if rerr != nil || strings.TrimSpace(string(b)) != "redirected" {
		t.Fatalf("file = %q, %v; want \"redirected\"", b, rerr)
	}
	if framed.String() != "" {
		t.Errorf("redirected output also reached the caller's writer: %q", framed.String())
	}
}

func TestWiringKeepsPipelinesTerminating(t *testing.T) {
	e := execd.NewShellExecutor()
	var out strings.Builder
	done := make(chan int, 1)
	go func() {
		code, _ := e.Run(context.Background(), "seq 1 100000 | head -n 3", t.TempDir(), nil, &out, io.Discard)
		done <- code
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("pipeline did not terminate")
	}
	if got := strings.Fields(out.String()); len(got) != 3 {
		t.Errorf("output = %q, want three lines", out.String())
	}
}

func TestWiringMergesWhenStdoutAndStderrAreOneWriter(t *testing.T) {
	e := execd.NewShellExecutor()
	var both strings.Builder
	if _, err := e.Run(context.Background(), "sh -c 'echo o; echo e >&2' 2>&1",
		t.TempDir(), nil, &both, &both); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(both.String(), "o") || !strings.Contains(both.String(), "e") {
		t.Errorf("merged output = %q, want both streams", both.String())
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
