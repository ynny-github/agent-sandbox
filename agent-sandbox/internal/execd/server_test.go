package execd_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

// echoExecutor writes a fixed reply and returns a fixed exit code, so the
// server can be tested without nono or any real process.
type echoExecutor struct {
	gotReq execd.Request
}

func (e *echoExecutor) Execute(ctx context.Context, req execd.Request,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	e.gotReq = req
	fmt.Fprintf(stdout, "ran %s in %s", req.Command, req.Cwd)
	fmt.Fprint(stderr, "warned")
	if stdin != nil {
		if b, _ := io.ReadAll(stdin); len(b) > 0 {
			fmt.Fprintf(stdout, " stdin=%s", b)
		}
	}
	return 7, nil
}

func startTestServer(t *testing.T, exec execd.Executor) string {
	t.Helper()
	// t.TempDir() embeds the (potentially long) test name in the path, which
	// on macOS can push a unix socket path past the ~104-byte sun_path limit
	// and make bind(2) fail with "invalid argument". Use a short, unrelated
	// temp dir for the socket instead.
	dir, err := os.MkdirTemp("", "brk")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "b.sock")
	srv, err := execd.NewServer(sock, exec)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestClientSendsTheCommandLine(t *testing.T) {
	echo := &echoExecutor{}
	sock := startTestServer(t, echo)

	var out, errb bytes.Buffer
	code, err := execd.NewClient(sock).RunCommand(
		context.Background(), "echo hi | cat", nil, &out, &errb, execd.RunOptions{})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if code != 7 {
		t.Errorf("exit = %d, want 7", code)
	}
	if echo.gotReq.Command != "echo hi | cat" {
		t.Errorf("server saw Command = %q, want %q", echo.gotReq.Command, "echo hi | cat")
	}
	if !strings.Contains(out.String(), "echo hi | cat") {
		t.Errorf("stdout = %q, want it to carry the command", out.String())
	}
}

func TestServerRunsCommandAndReturnsExitCode(t *testing.T) {
	echo := &echoExecutor{}
	sock := startTestServer(t, echo)

	c := execd.NewClient(sock)
	var out, errb testBuffer
	code, err := c.RunCommand(context.Background(),
		"go test", nil, &out, &errb, execd.RunOptions{})
	if err != nil {
		t.Fatalf("RunCommand() error = %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	if out.String() == "" || errb.String() != "warned" {
		t.Errorf("stdout = %q, stderr = %q", out.String(), errb.String())
	}
	if echo.gotReq.Command != "go test" {
		t.Errorf("server received command %q, want %q", echo.gotReq.Command, "go test")
	}
}

// blockingExecutor blocks until its context is cancelled, so a test can prove
// that a client disconnect reaches the executor.
type blockingExecutor struct {
	cancelled chan struct{}
}

func (b *blockingExecutor) Execute(ctx context.Context, req execd.Request,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	<-ctx.Done()
	close(b.cancelled)
	return 0, ctx.Err()
}

func TestServerCancelsCommandOnClientDisconnect(t *testing.T) {
	blocking := &blockingExecutor{cancelled: make(chan struct{})}
	sock := startTestServer(t, blocking)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	if err := execd.WriteRequest(conn, execd.Request{
		Command:         "sleep",
		Cwd:             "/",
		ProtocolVersion: execd.ProtocolVersion,
	}); err != nil {
		t.Fatalf("WriteRequest() error = %v", err)
	}
	conn.Close()

	select {
	case <-blocking.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("executor context was not cancelled after the client disconnected")
	}
}

func TestServerForwardsStdin(t *testing.T) {
	sock := startTestServer(t, &echoExecutor{})

	c := execd.NewClient(sock)
	var out, errb testBuffer
	_, err := c.RunCommand(context.Background(),
		"cat", stringsReader("piped"), &out, &errb, execd.RunOptions{})
	if err != nil {
		t.Fatalf("RunCommand() error = %v", err)
	}
	if !containsStr(out.String(), "stdin=piped") {
		t.Errorf("stdout = %q, want it to contain stdin=piped", out.String())
	}
}

// blockingReader never returns data and never reports EOF until it is
// released. It models the live upstream of a mixed pipeline such as
// `tail -f app.log | grep -m1 ERROR`: once grep matches and exits, tail is
// still running and sends nothing more, so execd sees neither a stdin
// frame nor a stdin-close frame.
type blockingReader struct{ release chan struct{} }

func (b *blockingReader) Read([]byte) (int, error) {
	<-b.release
	return 0, io.EOF
}

// Regression test for the execd deadlock: a command that exits without
// draining stdin must still produce an exit frame. Before the fix, os/exec's
// own stdin copier kept cmd.Wait blocked forever, so no exit frame was
// written and every caller — up to Claude's Bash tool — hung. "exit 5" is a
// shell builtin: the interpreter never touches stdin at all, which is exactly
// the case that must not block on the still-open request stdin below.
//
// The deadline makes this fail fast instead of hanging the suite.
func TestServerReportsExitWhenStdinNeverCloses(t *testing.T) {
	sock := startTestServer(t, execd.NewShellExecutor())

	stdin := &blockingReader{release: make(chan struct{})}
	t.Cleanup(func() { close(stdin.release) })

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		var out, errb testBuffer
		code, rerr := execd.NewClient(sock).RunCommand(
			context.Background(), "exit 5", stdin, &out, &errb, execd.RunOptions{})
		done <- result{code, rerr}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("RunCommand() error = %v", r.err)
		}
		if r.code != 5 {
			t.Errorf("exit code = %d, want 5", r.code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("RunCommand() did not return after the command exited: " +
			"execd is waiting on stdin that never ends")
	}
}

type testBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *testBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *testBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func stringsReader(s string) io.Reader { return strings.NewReader(s) }

func containsStr(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func TestServerRejectsUnknownProtocolVersion(t *testing.T) {
	sock := startTestServer(t, &echoExecutor{})
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := execd.WriteRequest(conn, execd.Request{
		Command:         "true",
		Cwd:             "/tmp",
		ProtocolVersion: execd.ProtocolVersion + 1,
	}); err != nil {
		t.Fatal(err)
	}
	f, err := execd.ReadFrame(conn)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if f.Channel != execd.ChanError {
		t.Fatalf("channel = %d, want ChanError", f.Channel)
	}
	if !strings.Contains(string(f.Payload), "protocol version") {
		t.Errorf("payload = %q, want it to name the protocol version", f.Payload)
	}
}

// gatedExecutor reports when Execute has started and then blocks until the
// test releases it. A direction rule can only be enforced while there is a
// request to enforce it on: against an executor that returns at once, the exit
// frame is written before watchConn has been scheduled to read anything, and
// the assertion becomes a race with the request's own completion rather than a
// test of the rule.
//
// started is closed once, so a second request against the same instance blocks
// on release like the first instead of panicking on a double close: a double
// that only survives one call is a trap for the next test to use it.
type gatedExecutor struct {
	started   chan struct{}
	startOnce sync.Once
	release   chan struct{}
}

func (g *gatedExecutor) Execute(ctx context.Context, req execd.Request,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	g.startOnce.Do(func() { close(g.started) })
	<-g.release
	return 0, nil
}

func TestServerRefusesAnOutboundChannelFromTheClient(t *testing.T) {
	gated := &gatedExecutor{started: make(chan struct{}), release: make(chan struct{})}
	sock := startTestServer(t, gated)
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := execd.WriteRequest(conn, execd.Request{
		Command: "true", Cwd: "/tmp", ProtocolVersion: execd.ProtocolVersion,
	}); err != nil {
		t.Fatal(err)
	}
	// The command is running and cannot finish until this test says so, which
	// is what keeps an exit frame from overtaking the frame sent below.
	<-gated.started
	defer close(gated.release)

	if err := execd.WriteFrame(conn, execd.ChanStdout, []byte("not mine to send")); err != nil {
		t.Fatal(err)
	}
	for {
		f, err := execd.ReadFrame(conn)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if f.Channel == execd.ChanError {
			if !strings.Contains(string(f.Payload), "channel") {
				t.Errorf("payload = %q, want it to name the channel", f.Payload)
			}
			return
		}
		if f.Channel == execd.ChanExit {
			t.Fatal("server accepted a frame the client may not send")
		}
	}
}

// TestServerForwardsSignalToTheCommand proves the whole path: a signal frame
// sent on a live connection reaches the process the request started. The
// command traps TERM and exits 42, a code it can only report if the signal was
// delivered — a killed-by-signal death would surface as 128+15 instead, and no
// delivery at all would leave it to finish its sleep.
//
// `sleep ... & wait` rather than a plain `sleep`: a shell waits for a
// foreground child before running its trap, so the trap would only fire once
// the sleep was over. The signal goes to the process group, so the shell and
// its backgrounded sleep both get it.
//
// The command announces itself on stdout and the signal is sent only once that
// frame has arrived, because a signal sent before the process exists reaches a
// Job with no groups in it and is correctly delivered to nothing. That is a
// handshake, not a sleep: nothing here depends on how long a fork takes.
func TestServerForwardsSignalToTheCommand(t *testing.T) {
	// Leave nothing behind on any path, this test's own failure included. The
	// sleep is distinctive so this cannot match anything else on the machine.
	t.Cleanup(func() { exec.Command("pkill", "-f", "sleep 5941").Run() })

	sock := startTestServer(t, execd.NewShellExecutor())
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := execd.WriteRequest(conn, execd.Request{
		Command:         "sh -c 'trap \"exit 42\" TERM; echo ready; sleep 5941 & wait'",
		Cwd:             t.TempDir(),
		ProtocolVersion: execd.ProtocolVersion,
	}); err != nil {
		t.Fatal(err)
	}
	// A signal that is never delivered leaves the command sleeping for over an
	// hour. The deadline turns that into a prompt failure rather than a hung
	// suite; it is not a timing assertion, so it is generous.
	if err := conn.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}

	signalled := false
	for {
		f, err := execd.ReadFrame(conn)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		switch f.Channel {
		case execd.ChanStdout:
			if !signalled && strings.Contains(string(f.Payload), "ready") {
				signalled = true
				if err := execd.WriteSignal(conn, syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
		case execd.ChanExit:
			if !signalled {
				t.Fatal("the command exited before it announced itself; nothing was signalled")
			}
			if f.ExitCode() != 42 {
				t.Errorf("exit = %d, want 42 from the trap", f.ExitCode())
			}
			return
		}
	}
}

// stdinReaderExecutor reads its stdin to completion and reports how that read
// ended. It is what makes a refusal path's treatment of the stdin pipe
// observable: when watchConn fails the pipe, this read returns an error; when
// the pipe is merely abandoned, the read blocks forever and the test's read
// deadline fires instead.
type stdinReaderExecutor struct {
	started   chan struct{}
	startOnce sync.Once
	readErr   chan error
}

func (s *stdinReaderExecutor) Execute(ctx context.Context, req execd.Request,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	s.startOnce.Do(func() { close(s.started) })
	_, err := io.ReadAll(stdin)
	s.readErr <- err
	return 0, nil
}

// TestServerRefusesASignalOutsideTheAllowList holds the read side of the
// allow-list to the same standard as the write side. WriteSignal refuses to
// send SIGUSR1, so this builds the frame by hand — which is exactly what a
// client that did not use this package would do.
//
// The request carries stdin, so the refusal path has a pipe to deal with: the
// executor is parked in a read of it, and a refusal that only cancelled the
// context would leave that read blocked on a pipe with no writer left alive.
// Asserting that the read woke with an error is what keeps the refusal path
// from quietly reverting to the abandon-the-pipe form.
func TestServerRefusesASignalOutsideTheAllowList(t *testing.T) {
	reader := &stdinReaderExecutor{started: make(chan struct{}), readErr: make(chan error, 1)}
	sock := startTestServer(t, reader)
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := execd.WriteRequest(conn, execd.Request{
		Command: "cat", Cwd: "/tmp", WithStdin: true,
		ProtocolVersion: execd.ProtocolVersion,
	}); err != nil {
		t.Fatal(err)
	}
	// As in the direction-rule test: the rule can only be observed while there
	// is a request to observe it on.
	<-reader.started

	if err := execd.WriteFrame(conn, execd.ChanSignal, []byte{byte(syscall.SIGUSR1)}); err != nil {
		t.Fatal(err)
	}
	// A server that drops the frame instead of refusing it sends nothing at
	// all, and the loop below would wait forever for a frame that is never
	// coming. The deadline makes that a failure rather than a hung suite.
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		f, err := execd.ReadFrame(conn)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if f.Channel == execd.ChanError {
			// SIGUSR1 is 10 on Linux and 30 on macOS, so the expected text is
			// built from the constant rather than written out.
			want := fmt.Sprintf("signal %d", int(syscall.SIGUSR1))
			if !strings.Contains(string(f.Payload), want) {
				t.Errorf("payload = %q, want it to contain %q", f.Payload, want)
			}
			break
		}
		if f.Channel == execd.ChanExit {
			t.Fatal("server accepted a signal outside the allow-list")
		}
	}

	select {
	case err := <-reader.readErr:
		if err == nil {
			t.Error("the executor's stdin read ended cleanly; " +
				"a refused request should fail the pipe, not close it as if the client were done")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the executor is still blocked reading stdin: " +
			"the refusal path abandoned the pipe instead of failing it")
	}
}

// closedStdinExecutor closes the request's stdin before anything is sent on it,
// so the next stdin frame the server relays fails to write, and then waits for
// a signal. It implements ExecuteWithSignals directly, which is also what makes
// the server's capability assertion visible to a test that starts no process.
type closedStdinExecutor struct {
	started chan struct{}
	got     chan syscall.Signal
}

func (c *closedStdinExecutor) Execute(ctx context.Context, req execd.Request,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	return c.ExecuteWithSignals(ctx, req, stdin, stdout, stderr, nil)
}

func (c *closedStdinExecutor) ExecuteWithSignals(ctx context.Context, req execd.Request,
	stdin io.Reader, stdout, stderr io.Writer, sigs <-chan syscall.Signal) (int, error) {
	if rc, ok := stdin.(io.Closer); ok {
		rc.Close() // every later write on the other end now fails
	}
	close(c.started)
	select {
	case sig := <-sigs:
		c.got <- sig
	case <-ctx.Done():
	}
	return 0, nil
}

// TestServerKeepsRelayingSignalsAfterAStdinWriteFails pins the one path in
// watchConn that does not end the request. A command that stops reading its
// stdin — it exited, or never read at all — says nothing about whether the
// request is over or the client is still there, so the reader has to survive
// it: it is the only route a signal frame has, and the only detector of a
// disconnect. A server that returned from the loop here would leave the
// request running and unsignallable for the rest of its life.
func TestServerKeepsRelayingSignalsAfterAStdinWriteFails(t *testing.T) {
	exe := &closedStdinExecutor{started: make(chan struct{}), got: make(chan syscall.Signal, 1)}
	sock := startTestServer(t, exe)
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := execd.WriteRequest(conn, execd.Request{
		Command: "cat", Cwd: "/tmp", WithStdin: true,
		ProtocolVersion: execd.ProtocolVersion,
	}); err != nil {
		t.Fatal(err)
	}
	<-exe.started

	if err := execd.WriteFrame(conn, execd.ChanStdin, []byte("nobody is reading this")); err != nil {
		t.Fatal(err)
	}
	if err := execd.WriteSignal(conn, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}

	select {
	case sig := <-exe.got:
		if sig != syscall.SIGINT {
			t.Errorf("signal = %v, want SIGINT", sig)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no signal reached the executor: the stdin write error stopped the connection reader")
	}
}
