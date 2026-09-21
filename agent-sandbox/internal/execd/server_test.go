package execd_test

import (
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
	stdio execd.Stdio) (int, error) {
	e.gotReq = req
	fmt.Fprintf(stdio.Out, "ran %s in %s", req.Command, req.Cwd)
	fmt.Fprint(stdio.Err, "warned")
	if b, _ := io.ReadAll(stdio.In); len(b) > 0 {
		fmt.Fprintf(stdio.Out, " stdin=%s", b)
	}
	return 7, nil
}

func startTestServer(t *testing.T, ex execd.Executor) string {
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
	srv, err := execd.NewServer(sock, ex)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	return sock
}

// outFile returns a file to pass as a command's stdout, and a func that reads
// back what landed in it. A request's output is no longer a writer this side
// holds, so a test that used to assert on a bytes.Buffer asserts on a file.
func outFile(t *testing.T) (*os.File, func() string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, func() string {
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
}

// inFile returns a file holding content, to pass as a command's stdin.
func inFile(t *testing.T, content string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// devNull is what a caller with nothing to send passes as stdin. Stdio has no
// branch for an absent file, which is the point: there is one path, not two.
func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// nullOut opens os.DevNull for writing, for a case that asserts nothing about
// what the command produced.
func nullOut(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// dialWithStdio opens a connection and hands it a request's three files, which
// is the first thing execd expects on any connection. It is what a test that
// drives the wire by hand needs before it can write a request at all.
func dialWithStdio(t *testing.T, sock string, stdio execd.Stdio) *net.UnixConn {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		t.Fatalf("Dial() returned %T, want *net.UnixConn", conn)
	}
	if err := execd.SendStdio(uc, stdio); err != nil {
		t.Fatalf("SendStdio() error = %v", err)
	}
	return uc
}

func TestClientSendsTheCommandLine(t *testing.T) {
	echo := &echoExecutor{}
	sock := startTestServer(t, echo)

	out, readOut := outFile(t)
	errf, _ := outFile(t)
	code, err := execd.NewClient(sock).RunCommand(
		context.Background(), "echo hi | cat",
		execd.Stdio{In: devNull(t), Out: out, Err: errf}, execd.RunOptions{})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if code != 7 {
		t.Errorf("exit = %d, want 7", code)
	}
	if echo.gotReq.Command != "echo hi | cat" {
		t.Errorf("server saw Command = %q, want %q", echo.gotReq.Command, "echo hi | cat")
	}
	if !strings.Contains(readOut(), "echo hi | cat") {
		t.Errorf("stdout = %q, want it to carry the command", readOut())
	}
}

func TestServerRunsCommandAndReturnsExitCode(t *testing.T) {
	echo := &echoExecutor{}
	sock := startTestServer(t, echo)

	c := execd.NewClient(sock)
	out, readOut := outFile(t)
	errf, readErr := outFile(t)
	code, err := c.RunCommand(context.Background(), "go test",
		execd.Stdio{In: devNull(t), Out: out, Err: errf}, execd.RunOptions{})
	if err != nil {
		t.Fatalf("RunCommand() error = %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	if readOut() == "" || readErr() != "warned" {
		t.Errorf("stdout = %q, stderr = %q", readOut(), readErr())
	}
	if echo.gotReq.Command != "go test" {
		t.Errorf("server received command %q, want %q", echo.gotReq.Command, "go test")
	}
}

// blockingExecutor blocks until its context is cancelled, so a test can prove
// that something which ends a request — a client disconnect, a refusal —
// reaches the executor. started is closed once rather than unconditionally, so
// a second request against the same instance blocks like the first instead of
// panicking on a double close.
type blockingExecutor struct {
	started   chan struct{}
	startOnce sync.Once
	cancelled chan struct{}
}

func newBlockingExecutor() *blockingExecutor {
	return &blockingExecutor{started: make(chan struct{}), cancelled: make(chan struct{})}
}

func (b *blockingExecutor) Execute(ctx context.Context, req execd.Request,
	stdio execd.Stdio) (int, error) {
	b.startOnce.Do(func() { close(b.started) })
	<-ctx.Done()
	close(b.cancelled)
	return 0, ctx.Err()
}

// TestServerCancelsCommandOnClientDisconnect is the disconnect detector, and
// it matters more now than it did: the client hands its descriptors away and
// then holds nothing but this connection, so closing it is the only thing left
// that tells execd the caller is gone. execd keeps its own copies of those
// three files for the whole request, and they are not what it watches —
// watchConn reads the control socket, exactly as before.
func TestServerCancelsCommandOnClientDisconnect(t *testing.T) {
	blocking := newBlockingExecutor()
	sock := startTestServer(t, blocking)

	conn := dialWithStdio(t, sock, execd.Stdio{
		In: devNull(t), Out: nullOut(t), Err: nullOut(t)})
	if err := execd.WriteRequest(conn, execd.Request{
		Command: "sleep",
		Cwd:     "/",
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

func TestServerGivesTheExecutorTheCallersStdin(t *testing.T) {
	sock := startTestServer(t, &echoExecutor{})

	c := execd.NewClient(sock)
	out, readOut := outFile(t)
	errf, _ := outFile(t)
	_, err := c.RunCommand(context.Background(), "cat",
		execd.Stdio{In: inFile(t, "piped"), Out: out, Err: errf}, execd.RunOptions{})
	if err != nil {
		t.Fatalf("RunCommand() error = %v", err)
	}
	if !strings.Contains(readOut(), "stdin=piped") {
		t.Errorf("stdout = %q, want it to contain stdin=piped", readOut())
	}
}

// A request whose stdin never delivers a byte and never ends must still report
// its exit status. The caller passes the read end of a pipe whose write end
// this test holds and neither writes to nor closes, which is the live upstream
// of a mixed pipeline such as `tail -f app.log | grep -m1 ERROR`: once grep
// matches and exits, tail is still running, so the input neither arrives nor
// finishes. "exit 5" is a shell builtin, so nothing in the request reads that
// stdin at all — the question is entirely whether anything on the path waits
// for it anyway.
//
// Three places could, and this covers all three at once: the interpreter, the
// server's handle, and the client's read loop. What it no longer covers is the
// defect it was written for — os/exec's own stdin copier keeping cmd.Wait
// blocked forever, which hung every caller up to Claude's Bash tool. That mode
// is unreachable twice over now: stdin is an *os.File, for which os/exec
// starts no copier, and a builtin forks nothing for Wait to be called on. The
// property outlived its original mechanism, which is why this stays.
//
// The deadline makes a regression fail fast instead of hanging the suite.
func TestServerReportsExitWhenStdinNeverCloses(t *testing.T) {
	sock := startTestServer(t, execd.NewShellExecutor())

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pr.Close(); pw.Close() })

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, rerr := execd.NewClient(sock).RunCommand(
			context.Background(), "exit 5",
			execd.Stdio{In: pr, Out: nullOut(t), Err: nullOut(t)}, execd.RunOptions{})
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
	stdio execd.Stdio) (int, error) {
	g.startOnce.Do(func() { close(g.started) })
	<-g.release
	return 0, nil
}

func TestServerRefusesAnOutboundChannelFromTheClient(t *testing.T) {
	gated := &gatedExecutor{started: make(chan struct{}), release: make(chan struct{})}
	sock := startTestServer(t, gated)
	conn := dialWithStdio(t, sock, execd.Stdio{
		In: devNull(t), Out: nullOut(t), Err: nullOut(t)})
	if err := execd.WriteRequest(conn, execd.Request{
		Command: "true", Cwd: "/tmp",
	}); err != nil {
		t.Fatal(err)
	}
	// The command is running and cannot finish until this test says so, which
	// is what keeps an exit frame from overtaking the frame sent below.
	<-gated.started
	defer close(gated.release)

	// ChanExit is a server-to-client channel; a client sending it is exactly
	// the direction violation this test exists to catch.
	if err := execd.WriteFrame(conn, execd.ChanExit, []byte("not mine to send")); err != nil {
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
// announcement has arrived, because a signal sent before the process exists
// reaches a Job with no groups in it and is correctly delivered to nothing.
// That is a handshake, not a sleep: nothing here depends on how long a fork
// takes. The announcement arrives on a pipe the test passes as the request's
// stdout and reads directly — there is no stdout frame to wait for any more,
// which is the point of the change this test now runs against.
func TestServerForwardsSignalToTheCommand(t *testing.T) {
	// Leave nothing behind on any path, this test's own failure included. The
	// sleep is distinctive so this cannot match anything else on the machine.
	t.Cleanup(func() { exec.Command("pkill", "-f", "sleep 5941").Run() })

	sock := startTestServer(t, execd.NewShellExecutor())

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pr.Close(); pw.Close() })

	conn := dialWithStdio(t, sock, execd.Stdio{In: devNull(t), Out: pw, Err: pw})
	// This side's copy of the write end, dropped once execd has its own: only
	// the command's copies should keep this pipe open from here.
	pw.Close()

	if err := execd.WriteRequest(conn, execd.Request{
		Command: "sh -c 'trap \"exit 42\" TERM; echo ready; sleep 5941 & wait'",
		Cwd:     t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	// A signal that is never delivered leaves the command sleeping for over an
	// hour. These deadlines turn that into a prompt failure rather than a hung
	// suite; they are not timing assertions, so they are generous.
	if err := pr.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}

	var announced strings.Builder
	buf := make([]byte, 64)
	for !strings.Contains(announced.String(), "ready") {
		n, rerr := pr.Read(buf)
		announced.Write(buf[:n])
		if rerr != nil {
			t.Fatalf("read the command's stdout: %v (got %q)", rerr, announced.String())
		}
	}
	if err := execd.WriteSignal(conn, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	for {
		f, err := execd.ReadFrame(conn)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if f.Channel == execd.ChanExit {
			if f.ExitCode() != 42 {
				t.Errorf("exit = %d, want 42 from the trap", f.ExitCode())
			}
			return
		}
	}
}

// TestServerRefusesASignalOutsideTheAllowList holds the read side of the
// allow-list to the same standard as the write side. WriteSignal refuses to
// send SIGUSR1, so this builds the frame by hand — which is exactly what a
// client that did not use this package would do.
//
// It also pins what a refusal does to the request. Every path out of watchConn
// now ends the same way, by cancelling the request's context, so asserting
// that the executor's context was cancelled is what keeps a refusal from
// quietly becoming a frame the server writes and then ignores.
func TestServerRefusesASignalOutsideTheAllowList(t *testing.T) {
	blocking := newBlockingExecutor()
	sock := startTestServer(t, blocking)
	conn := dialWithStdio(t, sock, execd.Stdio{
		In: devNull(t), Out: nullOut(t), Err: nullOut(t)})
	if err := execd.WriteRequest(conn, execd.Request{
		Command: "cat", Cwd: "/tmp",
	}); err != nil {
		t.Fatal(err)
	}
	// The rule can only be observed while there is a request to observe it on.
	<-blocking.started

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
	case <-blocking.cancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("the executor is still running: a refused request must end, " +
			"not merely have its refusal reported")
	}
}

// signalWatchExecutor implements ExecuteWithSignals directly, which is what
// makes the server's capability assertion visible to a test that starts no
// process. The assertion is by interface value rather than by widening
// Executor, so a double like this one — which knows nothing about signals in
// its Execute — stays a valid Executor.
type signalWatchExecutor struct {
	started chan struct{}
	got     chan syscall.Signal
}

func (c *signalWatchExecutor) Execute(ctx context.Context, req execd.Request,
	stdio execd.Stdio) (int, error) {
	return c.ExecuteWithSignals(ctx, req, stdio, nil)
}

func (c *signalWatchExecutor) ExecuteWithSignals(ctx context.Context, req execd.Request,
	stdio execd.Stdio, sigs <-chan syscall.Signal) (int, error) {
	close(c.started)
	select {
	case sig := <-sigs:
		c.got <- sig
	case <-ctx.Done():
	}
	return 0, nil
}

// TestServerRelaysASignalToAnExecutorThatWantsOne pins the capability
// assertion in handle: an executor that implements ExecuteWithSignals is
// reached through it and gets the channel. The signature it is asserted
// against carries a Stdio, so an executor left on the old three-stream shape
// would silently fall back to Execute and never see a signal again — a
// regression that no test of the real ShellExecutor could tell apart from a
// signal that simply did not arrive.
func TestServerRelaysASignalToAnExecutorThatWantsOne(t *testing.T) {
	exe := &signalWatchExecutor{started: make(chan struct{}), got: make(chan syscall.Signal, 1)}
	sock := startTestServer(t, exe)
	conn := dialWithStdio(t, sock, execd.Stdio{
		In: devNull(t), Out: nullOut(t), Err: nullOut(t)})
	if err := execd.WriteRequest(conn, execd.Request{
		Command: "cat", Cwd: "/tmp",
	}); err != nil {
		t.Fatal(err)
	}
	<-exe.started

	if err := execd.WriteSignal(conn, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case sig := <-exe.got:
		if sig != syscall.SIGINT {
			t.Errorf("signal = %v, want SIGINT", sig)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no signal reached the executor")
	}
}

// The end-to-end shape: a real server, a real command, and the caller's own
// file on the other end of the descriptor.
func TestCommandWritesThroughThePassedDescriptor(t *testing.T) {
	sock := startTestServer(t, execd.NewShellExecutor())
	c := execd.NewClient(sock)

	dir := t.TempDir()
	out, err := os.Create(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()

	code, err := c.RunCommand(context.Background(), "echo through-the-descriptor",
		execd.Stdio{In: devnull, Out: out, Err: out}, execd.RunOptions{})
	if err != nil || code != 0 {
		t.Fatalf("RunCommand = %d, %v", code, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "through-the-descriptor" {
		t.Errorf("file = %q, want the command's output", b)
	}
}

// Stdin arrives the same way, with no frames involved.
func TestCommandReadsThroughThePassedDescriptor(t *testing.T) {
	sock := startTestServer(t, execd.NewShellExecutor())
	c := execd.NewClient(sock)

	dir := t.TempDir()
	inPath := filepath.Join(dir, "in")
	if err := os.WriteFile(inPath, []byte("fed-through\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(inPath)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	code, err := c.RunCommand(context.Background(), "cat",
		execd.Stdio{In: in, Out: out, Err: out}, execd.RunOptions{})
	if err != nil || code != 0 {
		t.Fatalf("RunCommand = %d, %v", code, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "fed-through" {
		t.Errorf("file = %q, want the stdin the caller passed", b)
	}
}

// execd must close its copies when the request ends, or a caller that passed a
// pipe's write end waits forever. This is the ownership rule the spec names as
// the one a future edit is most likely to break.
func TestServerClosesItsCopiesWhenTheRequestEnds(t *testing.T) {
	sock := startTestServer(t, execd.NewShellExecutor())
	c := execd.NewClient(sock)

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pr.Close(); pw.Close() })
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()

	if _, err := c.RunCommand(context.Background(), "echo done",
		execd.Stdio{In: devnull, Out: pw, Err: pw}, execd.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	pw.Close() // this side's copy; only execd's may be keeping the pipe open now

	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(pr)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("read to EOF: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no EOF: execd leaked its copy of the passed descriptor")
	}
}

// TestServerRefusesARequestThatArrivesWithNoDescriptors is the old-client /
// new-server half of the structural mismatch detection, driven through a real
// server rather than through RecvStdio alone.
//
// That detection is the entire argument for deleting Request.ProtocolVersion:
// the field existed because encoding/json drops unknown fields, so an older
// execd would silently ignore a newer field — running a command with no
// timeout at all. It is droppable only because this protocol's first move is a
// descriptor handshake an older binary can neither send nor read, and a
// mismatched pair is reachable here: the launcher starts execd from its own
// absolute path while the sandboxed client resolves agent-sandbox through
// PATH, so rebuilding mid-session produces exactly this pair.
//
// An old client's first move is the request itself, which is what this sends:
// dial, write a request with no control message attached. RecvStdio's own
// tests cover the same rejection over a socketpair; this one proves the server
// applies it, turns it into an error frame, and that the frame carries the
// remedy — restart the session — rather than a protocol complaint the reader
// cannot act on.
func TestServerRefusesARequestThatArrivesWithNoDescriptors(t *testing.T) {
	sock := startTestServer(t, &echoExecutor{})
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	// Deliberately not dialWithStdio: sending no descriptors is the case.
	if err := execd.WriteRequest(conn, execd.Request{
		Command: "true", Cwd: "/tmp",
	}); err != nil {
		t.Fatal(err)
	}

	f, err := execd.ReadFrame(conn)
	if err != nil {
		t.Fatalf("read the server's reply: %v", err)
	}
	if f.Channel != execd.ChanError {
		t.Fatalf("channel = %d, want ChanError (%d): the server served a "+
			"request that arrived without descriptors", f.Channel, execd.ChanError)
	}
	if !strings.Contains(string(f.Payload), "restart the session") {
		t.Errorf("payload = %q, want it to tell the reader to restart the "+
			"session; a mismatched binary pair is the cause and that is the "+
			"only remedy", f.Payload)
	}
}
