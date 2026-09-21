package execd_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

// TestPassedStdinCannotBeInterruptedWhileBlockedOnARead pins a limitation, not
// a guarantee: a command blocked reading the stdin the client passed is
// uninterruptible, and the request's own timeout does not end it. See Stdio in
// protocol.go for the mechanism and for why it is documented rather than
// fixed. This test exists so the next person to meet it finds it named here
// instead of debugging a mystery hang.
//
// The shape it pins, exactly:
//
//	a request whose stdin is a pipe nobody writes to, with TimeoutMs 700,
//	has NOT returned 2s later — and returns as soon as the write end closes.
//
// Both halves matter. The first is the defect; the second is what keeps this
// test honest, because a test that only asserted "does not return" would pass
// just as well against a request that was broken in some other way.
//
// It is bounded in both directions so a regression fails fast rather than
// hanging the package: the pin is a 2s window (the timeout it outlives is
// 700ms), and the release is given 20s. If someone later makes the read
// interruptible, this test fails at the 2s mark with a message saying so —
// which is the correct outcome, and the test to delete then.
func TestPassedStdinCannotBeInterruptedWhileBlockedOnARead(t *testing.T) {
	sock := startTestServer(t, execd.NewShellExecutor())
	c := execd.NewClient(sock)

	// A pipe with no writer but this test. `read x` parks in a one-byte read
	// on it and stays there.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// The write end must be closed on every path, the failing ones included:
	// while it is open, the interpreter's read never ends, so execd's handler
	// goroutine and its three descriptors would be held for the rest of this
	// test binary's life. sync.Once because the success path closes it early
	// and the cleanup closes it again.
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { pw.Close() }) }
	t.Cleanup(release)
	t.Cleanup(func() { pr.Close() })

	// Opened here rather than inside the goroutine: nullOut calls t.Cleanup
	// and t.Fatal, neither of which may be used from a non-test goroutine.
	out, errOut := nullOut(t), nullOut(t)

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		code, rerr := c.RunCommand(context.Background(), "read x",
			execd.Stdio{In: pr, Out: out, Err: errOut},
			execd.RunOptions{TimeoutMs: 700})
		done <- result{code, rerr}
	}()

	select {
	case r := <-done:
		t.Fatalf("RunCommand returned (%d, %v) after %v: a blocked read on a "+
			"passed stdin became interruptible. That is an improvement, not a "+
			"failure — delete this test and the paragraph on execd.Stdio it "+
			"pins.", r.code, r.err, time.Since(start))
	case <-time.After(2 * time.Second):
		// Still blocked, 1.3s past its own 700ms timeout. This is the pin.
	}

	release()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("RunCommand after the read was released: %v", r.err)
		}
		// The timeout did fire — it just could not end the read. The status it
		// set is what surfaces once the read returns.
		if r.code != 124 {
			t.Errorf("exit = %d, want 124: the timeout fired while the read "+
				"was blocked and is what the request reports", r.code)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("RunCommand did not return after the write end was closed; " +
			"the read did not even end at EOF")
	}
}

// TestClientRefusesAnInboundChannelFromTheServer is the client's half of the
// direction rule — the mirror of TestServerRefusesAnOutboundChannelFromTheClient,
// which had no counterpart on this side. Only an exit and an error are ever
// sent to a client, so anything else is a peer that is not speaking this
// protocol, and reading it as data would leave a desynced stream to surface
// later as something harder to read.
//
// It needs a server that misbehaves, so it uses a hand-written listener rather
// than execd.Server, which by construction cannot produce the frame under
// test.
func TestClientRefusesAnInboundChannelFromTheServer(t *testing.T) {
	// A short temp dir, not t.TempDir(): the test name would push the socket
	// path past sun_path's ~104 bytes on macOS. Same reason as startTestServer.
	dir, err := os.MkdirTemp("", "brk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "b.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })

	served := make(chan error, 1)
	go func() {
		conn, aerr := l.Accept()
		if aerr != nil {
			served <- aerr
			return
		}
		defer conn.Close()
		// Consume the handshake and the request the client sends, so the frame
		// below is written onto a connection in the state a real one would be
		// in. The received descriptors are closed at once; this fake runs no
		// command and holding them would leave the client's files open.
		stdio, rerr := execd.RecvStdio(conn.(*net.UnixConn))
		if rerr != nil {
			served <- rerr
			return
		}
		stdio.Close()
		if _, rerr := execd.ReadRequest(conn); rerr != nil {
			served <- rerr
			return
		}
		// ChanSignal is a client-to-server channel. A server sending it is the
		// direction violation under test.
		served <- execd.WriteFrame(conn, execd.ChanSignal, []byte{byte(syscall.SIGINT)})
	}()

	code, err := execd.NewClient(sock).RunCommand(context.Background(), "true",
		execd.Stdio{In: devNull(t), Out: nullOut(t), Err: nullOut(t)},
		execd.RunOptions{})
	if serr := <-served; serr != nil {
		t.Fatalf("the fake server failed before the client could judge it: %v", serr)
	}
	if err == nil {
		t.Fatalf("RunCommand = %d, nil; want a refusal of a channel the "+
			"server may not send", code)
	}
	if !strings.Contains(err.Error(), "may not be sent by the server") {
		t.Errorf("error = %q, want it to name the direction rule", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("channel %d", execd.ChanSignal)) {
		t.Errorf("error = %q, want it to name channel %d", err, execd.ChanSignal)
	}
}
