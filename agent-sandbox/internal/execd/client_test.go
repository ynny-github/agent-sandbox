package execd_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/execd"
)

func TestExecWiresStdin(t *testing.T) {
	sock := startTestServer(t, execd.NewShellExecutor())
	c := execd.NewClient(sock)
	out, readOut := outFile(t)
	code, err := c.RunCommand(context.Background(), "cat",
		execd.Stdio{In: inFile(t, "fed-through\n"), Out: out, Err: nullOut(t)},
		execd.RunOptions{})
	if err != nil || code != 0 {
		t.Fatalf("RunCommand = %d, %v", code, err)
	}
	if strings.TrimSpace(readOut()) != "fed-through" {
		t.Errorf("out = %q, want \"fed-through\"", readOut())
	}
}

// A wiring regression here — the timeout never reaching the request — must
// fail fast rather than only be caught once "sleep 30" finishes on its own,
// which is why this asserts the elapsed time is bounded well under 30s and
// not just the exit code.
func TestExecForwardsTimeout(t *testing.T) {
	sock := startTestServer(t, execd.NewShellExecutor())
	c := execd.NewClient(sock)
	start := time.Now()
	code, err := c.RunCommand(context.Background(), "sleep 30",
		execd.Stdio{In: devNull(t), Out: nullOut(t), Err: nullOut(t)},
		execd.RunOptions{TimeoutMs: 400})
	if err != nil {
		t.Fatal(err)
	}
	if code != 124 {
		t.Errorf("exit = %d, want 124", code)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v; want it bounded by TimeoutMs", d)
	}
}

// TestExecRelaysASignalWhileTheCommandRuns is the client-level end of the
// signal path: a signal handed to RunOptions.Signals while the command is
// running reaches it. The command can only report exit 42 by actually
// trapping the signal, so a regression shows up as a wrong exit code, a
// RunCommand error, or (bounded by ctx) a hang.
//
// It replaces TestExecSignalDuringActiveStdin, which sent that signal while
// the client's stdin pump was writing frames on the same connection. That
// scenario no longer exists: stdin is a descriptor the command reads itself,
// so signal forwarding is the only thing left that writes to this connection.
// What remains testable, and what this covers, is the forwarder itself.
//
// The command is kept alive by a stdin that never delivers a byte and never
// ends — the read end of a pipe this test holds the write end of — so the
// trap has something to interrupt, and it announces itself on a pipe passed
// as its stdout so the signal is sent only once the trap is armed.
func TestExecRelaysASignalWhileTheCommandRuns(t *testing.T) {
	// Distinctive marker so cleanup cannot match an unrelated process; belt
	// and suspenders alongside the ctx timeout below, which already causes
	// execd to kill the command's process group when the connection closes.
	t.Cleanup(func() { exec.Command("pkill", "-f", "execd-signal-relay-probe").Run() })

	sock := startTestServer(t, execd.NewShellExecutor())
	c := execd.NewClient(sock)

	inr, inw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inr.Close(); inw.Close() })
	outr, outw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { outr.Close(); outw.Close() })

	relay := make(chan syscall.Signal, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// The backgrounded cat reads the same stdin the shell was given, so it is
	// what keeps the pipeline alive; `& wait` (rather than a foreground cat)
	// is what lets the trap fire before stdin ever closes, the same technique
	// TestServerForwardsSignalToTheCommand uses with sleep. The leading no-op
	// ": execd-signal-relay-probe" puts a distinctive, harmless token into the
	// process's own argv so cleanup's pkill -f can find it.
	command := `sh -c ': execd-signal-relay-probe; trap "exit 42" TERM; echo ready; ` +
		`cat >/dev/null & wait'`

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		code, rerr := c.RunCommand(ctx, command,
			execd.Stdio{In: inr, Out: outw, Err: outw}, execd.RunOptions{Signals: relay})
		done <- result{code, rerr}
	}()

	if err := outr.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var announced strings.Builder
	buf := make([]byte, 64)
	for !strings.Contains(announced.String(), "ready") {
		n, rerr := outr.Read(buf)
		announced.Write(buf[:n])
		if rerr != nil {
			t.Fatalf("read the command's stdout: %v (got %q)", rerr, announced.String())
		}
	}
	relay <- syscall.SIGTERM

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("RunCommand: %v", r.err)
		}
		if r.code != 42 {
			t.Errorf("exit = %d, want 42 from the trap", r.code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("RunCommand did not return; the relayed signal never reached the command")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %v; a signal that reaches the command ends it promptly", d)
	}
}
