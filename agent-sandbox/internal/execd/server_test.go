package execd_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	exec := &echoExecutor{}
	sock := startTestServer(t, exec)

	var out, errb bytes.Buffer
	code, err := execd.NewClient(sock).RunCommand(
		context.Background(), "echo hi | cat", nil, &out, &errb)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if code != 7 {
		t.Errorf("exit = %d, want 7", code)
	}
	if exec.gotReq.Command != "echo hi | cat" {
		t.Errorf("server saw Command = %q, want %q", exec.gotReq.Command, "echo hi | cat")
	}
	if !strings.Contains(out.String(), "echo hi | cat") {
		t.Errorf("stdout = %q, want it to carry the command", out.String())
	}
}

func TestServerRunsCommandAndReturnsExitCode(t *testing.T) {
	exec := &echoExecutor{}
	sock := startTestServer(t, exec)

	c := execd.NewClient(sock)
	var out, errb testBuffer
	code, err := c.RunCommand(context.Background(),
		"go test", nil, &out, &errb)
	if err != nil {
		t.Fatalf("RunCommand() error = %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	if out.String() == "" || errb.String() != "warned" {
		t.Errorf("stdout = %q, stderr = %q", out.String(), errb.String())
	}
	if exec.gotReq.Command != "go test" {
		t.Errorf("server received command %q, want %q", exec.gotReq.Command, "go test")
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
	exec := &blockingExecutor{cancelled: make(chan struct{})}
	sock := startTestServer(t, exec)

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
	case <-exec.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("executor context was not cancelled after the client disconnected")
	}
}

func TestServerForwardsStdin(t *testing.T) {
	sock := startTestServer(t, &echoExecutor{})

	c := execd.NewClient(sock)
	var out, errb testBuffer
	_, err := c.RunCommand(context.Background(),
		"cat", stringsReader("piped"), &out, &errb)
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
			context.Background(), "exit 5", stdin, &out, &errb)
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
type gatedExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (g *gatedExecutor) Execute(ctx context.Context, req execd.Request,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	close(g.started)
	<-g.release
	return 0, nil
}

func TestServerRefusesAnOutboundChannelFromTheClient(t *testing.T) {
	exec := &gatedExecutor{started: make(chan struct{}), release: make(chan struct{})}
	sock := startTestServer(t, exec)
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
	<-exec.started
	defer close(exec.release)

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
