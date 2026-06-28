package router_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
)

// echoRunner copies its stdin to stdout (models `cat` in the container).
type echoRunner struct{ gotArgv [][]string }

func (e *echoRunner) RunContainer(_ context.Context, argv, _ []string, stdin io.Reader, stdout, _ io.Writer) (int, error) {
	e.gotArgv = append(e.gotArgv, argv)
	if stdin != nil {
		io.Copy(stdout, stdin)
	}
	return 0, nil
}

func TestRun_MixedPipe_HostToContainer(t *testing.T) {
	// `echo hi` on host (allowed) | `cat` in container.
	r := &echoRunner{}
	var out, errb bytes.Buffer
	code, err := router.Run(context.Background(), router.Request{
		Command:         "echo hi | cat",
		AllowPatterns:   []string{"echo *"}, // echo→host, cat→container
		ContainerRunner: r,
		Stdout:          &out, Stderr: &errb,
	})
	if err != nil {
		t.Fatalf("err = %v stderr=%q", err, errb.String())
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if out.String() != "hi\n" {
		t.Fatalf("stdout = %q, want %q", out.String(), "hi\n")
	}
	// Container segment ran as argv (cat), not bash -c the whole pipeline.
	if len(r.gotArgv) != 1 || r.gotArgv[0][0] == "bash" {
		t.Fatalf("container argv = %#v, want a single non-bash cat call", r.gotArgv)
	}
}

// earlyExitRunner reads exactly one line from stdin then returns, without draining.
// This simulates a downstream command like `head -1` that exits before its upstream finishes.
type earlyExitRunner struct{}

func (e *earlyExitRunner) RunContainer(_ context.Context, _ []string, _ []string, stdin io.Reader, stdout, _ io.Writer) (int, error) {
	if stdin != nil {
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		stdout.Write([]byte(line))
	}
	// Return immediately WITHOUT draining the rest of stdin.
	return 0, nil
}

// TestRun_MixedPipe_EarlyExitDeadlockGuard is a regression test for the
// mixed-pipeline deadlock: if the downstream container segment exits before
// draining stdin, the upstream host segment must unblock and the call must
// return promptly. Without closing the pipe read end this would hang forever.
func TestRun_MixedPipe_EarlyExitDeadlockGuard(t *testing.T) {
	// `seq 1 1000000` on host produces a large stream; the container segment
	// reads only the first line and returns. The read-end close must unblock
	// seq's io.Copy so the pipeline returns without deadlocking.
	var out, errb bytes.Buffer
	done := make(chan struct{})
	var (
		runCode int
		runErr  error
	)
	go func() {
		defer close(done)
		runCode, runErr = router.Run(context.Background(), router.Request{
			Command:         "seq 1 1000000 | cat",
			AllowPatterns:   []string{"seq *"}, // seq→host, cat→container
			ContainerRunner: &earlyExitRunner{},
			Stdout:          &out,
			Stderr:          &errb,
		})
	}()
	select {
	case <-done:
		// Completed promptly — no deadlock.
	case <-time.After(10 * time.Second):
		t.Fatal("runMixedPipeline deadlocked: did not return within 10s (pipe read end not closed on early exit)")
	}
	if runErr != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", runErr, errb.String())
	}
	_ = runCode // exit code may be non-zero due to broken pipe; that's fine
}

// TestRun_MixedPipe_ThreeSegments verifies that a 3-segment host|container|host
// pipeline correctly wires all inter-segment pipes, closing both ends on finish.
// Routing: "echo *" and "tr *" → host; middle segment → container (echoRunner).
func TestRun_MixedPipe_ThreeSegments(t *testing.T) {
	// echo hello | <container: copy stdin→stdout> | tr a-z A-Z
	// Expected: "HELLO\n"
	r := &echoRunner{}
	var out, errb bytes.Buffer
	code, err := router.Run(context.Background(), router.Request{
		Command:         "echo hello | cat | tr a-z A-Z",
		AllowPatterns:   []string{"echo *", "tr *"}, // echo+tr→host, cat→container
		ContainerRunner: r,
		Stdout:          &out,
		Stderr:          &errb,
	})
	if err != nil {
		t.Fatalf("err = %v stderr=%q", err, errb.String())
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0 (stderr=%q)", code, errb.String())
	}
	want := "HELLO\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
}
