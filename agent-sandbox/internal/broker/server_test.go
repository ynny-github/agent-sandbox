package broker_test

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

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
)

// echoExecutor writes a fixed reply and returns a fixed exit code, so the
// server can be tested without nono or any real process.
type echoExecutor struct {
	gotReq broker.Request
}

func (e *echoExecutor) Execute(ctx context.Context, req broker.Request,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	e.gotReq = req
	fmt.Fprintf(stdout, "ran %v in %s", req.Argv, req.Cwd)
	fmt.Fprint(stderr, "warned")
	if stdin != nil {
		if b, _ := io.ReadAll(stdin); len(b) > 0 {
			fmt.Fprintf(stdout, " stdin=%s", b)
		}
	}
	return 7, nil
}

func startTestServer(t *testing.T, exec broker.Executor) string {
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
	srv, err := broker.NewServer(sock, exec)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestServerRunsCommandAndReturnsExitCode(t *testing.T) {
	exec := &echoExecutor{}
	sock := startTestServer(t, exec)

	c := broker.NewClient(sock)
	var out, errb testBuffer
	code, err := c.RunSandboxed(context.Background(),
		[]string{"go", "test"}, []string{"A=1"}, nil, &out, &errb)
	if err != nil {
		t.Fatalf("RunSandboxed() error = %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	if out.String() == "" || errb.String() != "warned" {
		t.Errorf("stdout = %q, stderr = %q", out.String(), errb.String())
	}
	if len(exec.gotReq.Env) != 1 || exec.gotReq.Env[0] != "A=1" {
		t.Errorf("server received env %v, want [A=1]", exec.gotReq.Env)
	}
}

// blockingExecutor blocks until its context is cancelled, so a test can prove
// that a client disconnect reaches the executor.
type blockingExecutor struct {
	cancelled chan struct{}
}

func (b *blockingExecutor) Execute(ctx context.Context, req broker.Request,
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
	if err := broker.WriteRequest(conn, broker.Request{Argv: []string{"sleep"}, Cwd: "/"}); err != nil {
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

	c := broker.NewClient(sock)
	var out, errb testBuffer
	_, err := c.RunSandboxed(context.Background(),
		[]string{"cat"}, nil, stringsReader("piped"), &out, &errb)
	if err != nil {
		t.Fatalf("RunSandboxed() error = %v", err)
	}
	if !containsStr(out.String(), "stdin=piped") {
		t.Errorf("stdout = %q, want it to contain stdin=piped", out.String())
	}
}

// blockingReader never returns data and never reports EOF until it is
// released. It models the live upstream of a mixed pipeline such as
// `tail -f app.log | grep -m1 ERROR`: once grep matches and exits, tail is
// still running and sends nothing more, so the broker sees neither a stdin
// frame nor a stdin-close frame.
type blockingReader struct{ release chan struct{} }

func (b *blockingReader) Read([]byte) (int, error) {
	<-b.release
	return 0, io.EOF
}

// Regression test for the broker deadlock: a sandboxed command that exits
// without draining stdin must still produce an exit frame. Before the fix,
// os/exec's own stdin copier kept cmd.Wait blocked forever, so no exit frame
// was written and every caller — up to Claude's Bash tool — hung.
//
// The deadline makes this fail fast instead of hanging the suite.
func TestServerReportsExitWhenStdinNeverCloses(t *testing.T) {
	// The client sends its own working directory, which the executor validates.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	// A stub "nono" whose child exits immediately without reading stdin.
	stub := writeStub(t, "#!/bin/sh\nexit 5\n")
	sock := startTestServer(t, broker.NewNonoExecutor(stub, "/tmp/p.json", cwd, nil))

	stdin := &blockingReader{release: make(chan struct{})}
	t.Cleanup(func() { close(stdin.release) })

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		var out, errb testBuffer
		code, rerr := broker.NewClient(sock).RunSandboxed(
			context.Background(), []string{"grep", "-m1", "ERROR"}, nil, stdin, &out, &errb)
		done <- result{code, rerr}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("RunSandboxed() error = %v", r.err)
		}
		if r.code != 5 {
			t.Errorf("exit code = %d, want 5", r.code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("RunSandboxed() did not return after the sandboxed command exited: " +
			"the broker is waiting on stdin that never ends")
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
