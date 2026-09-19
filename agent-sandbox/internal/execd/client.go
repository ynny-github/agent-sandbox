package execd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
)

// ErrExecdUnavailable signals that the execd socket could not be reached.
// Callers translate it into an actionable message instead of a raw dial error.
var ErrExecdUnavailable = errors.New("exec daemon is not available")

// SocketEnvVar names the environment variable that carries the execd socket
// path into the sandbox.
const SocketEnvVar = "AGENT_SANDBOX_EXECD_SOCKET"

// Client dials the execd socket. It is safe for concurrent use: every call
// opens its own connection, which is what lets a mixed pipeline run several
// sandboxed segments at once.
type Client struct {
	sockPath string
}

// NewClient returns a client for the socket at sockPath.
func NewClient(sockPath string) *Client { return &Client{sockPath: sockPath} }

// NewClientFromEnv builds a client from SocketEnvVar. It returns
// ErrExecdUnavailable when the variable is unset, which happens whenever a
// command is routed to the sandbox outside an `agent-sandbox claude` session.
func NewClientFromEnv() (*Client, error) {
	path := os.Getenv(SocketEnvVar)
	if path == "" {
		return nil, fmt.Errorf("%w: %s is not set", ErrExecdUnavailable, SocketEnvVar)
	}
	return NewClient(path), nil
}

// RunOptions carries the per-request knobs that are not the command itself.
type RunOptions struct {
	// TimeoutMs bounds the request; zero means no bound.
	TimeoutMs int
	// Signals, when non-nil, is drained for the life of the request and each
	// signal is forwarded to the command.
	Signals <-chan syscall.Signal
}

// RunCommand sends one command line to execd and streams its output back.
func (c *Client) RunCommand(ctx context.Context, command string,
	stdin io.Reader, stdout, stderr io.Writer, opts RunOptions) (int, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.sockPath)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrExecdUnavailable, err)
	}
	defer conn.Close()

	// Cancel the in-flight command by closing the connection; the server sees
	// EOF and tears the child down with it.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	req := Request{
		Command:         command,
		Cwd:             workingDir(),
		WithStdin:       stdin != nil,
		ProtocolVersion: ProtocolVersion,
		TimeoutMs:       opts.TimeoutMs,
	}
	if err := WriteRequest(conn, req); err != nil {
		return 0, err
	}

	// cw serializes every frame this client writes to conn. WriteFrame issues
	// a header Write followed by a payload Write for a non-empty payload; it
	// is not atomic. The stdin pump and the signal forwarder below both write
	// frames from their own goroutines, so without a shared lock a signal
	// frame could land between a stdin frame's header and payload writes.
	// The server trusts the declared length and would read the signal
	// frame's bytes as stdin payload, desyncing the whole stream for the
	// rest of the request — exactly the case where someone interrupts a
	// command that is still streaming input. cw is the client-side
	// counterpart of the server's frameWriter, which serializes its own
	// concurrent frame writes the same way.
	cw := &connWriter{c: conn}

	if stdin != nil {
		go pumpStdinTo(cw, stdin)
	}

	// The forwarder is scoped to this request: it exits via done (closed by
	// the deferred close above) as soon as RunCommand returns, and it never
	// blocks the caller because opts.Signals is only read here, never
	// written to — a caller that never signals leaves this select parked on
	// two channels neither of which it owns the pace of.
	if opts.Signals != nil {
		go func() {
			for {
				select {
				case sig := <-opts.Signals:
					// A write failure here means the connection is already
					// gone — the peer closed it, or the ctx.Done() goroutine
					// above closed it first. The read loop below observes
					// the same failure via ReadFrame and turns it into
					// RunCommand's returned error, so there is nothing
					// further to report from this side.
					cw.writeSignal(sig)
				case <-done:
					return
				}
			}
		}()
	}

	for {
		f, err := ReadFrame(conn)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, fmt.Errorf("execd: connection closed before exit status")
			}
			return 0, err
		}
		switch f.Channel {
		case ChanStdout:
			if _, werr := stdout.Write(f.Payload); werr != nil {
				return 0, fmt.Errorf("execd: write stdout: %w", werr)
			}
		case ChanStderr:
			if _, werr := stderr.Write(f.Payload); werr != nil {
				return 0, fmt.Errorf("execd: write stderr: %w", werr)
			}
		case ChanExit:
			return f.ExitCode(), nil
		case ChanError:
			return 0, fmt.Errorf("execd: %s", f.Payload)
		}
	}
}

func pumpStdinTo(cw *connWriter, stdin io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := stdin.Read(buf)
		if n > 0 {
			if werr := cw.writeFrame(ChanStdin, buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			cw.writeFrame(ChanStdinClose, nil)
			return
		}
	}
}

// connWriter serializes concurrent frame writes onto the one connection a
// request uses. It is the client-side counterpart of the server's
// frameWriter (server.go), which exists for the same reason: two goroutines
// — here, the stdin pump and the signal forwarder — must not interleave
// their writes to the same net.Conn, since a partially written frame would
// desync the peer's read of the whole stream.
type connWriter struct {
	mu sync.Mutex
	c  net.Conn
}

func (cw *connWriter) writeFrame(ch Channel, payload []byte) error {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	return WriteFrame(cw.c, ch, payload)
}

func (cw *connWriter) writeSignal(sig syscall.Signal) error {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	return WriteSignal(cw.c, sig)
}

func workingDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// CommandRunner executes one command line inside the sandbox. The execd client
// is the production implementation; tests substitute their own.
type CommandRunner interface {
	RunCommand(ctx context.Context, command string, stdin io.Reader,
		stdout, stderr io.Writer, opts RunOptions) (int, error)
}

// SandboxNotRunningHint is the actionable message shown when execd is not
// reachable. It is exported because the situation is detected before any
// command runs: `agent-sandbox exec` and the MCP server both fail to build a
// client when AGENT_SANDBOX_EXECD_SOCKET is unset, and must print this rather
// than a raw dial error.
const SandboxNotRunningHint = "exec daemon is not available; run Claude via `agent-sandbox claude`, which starts it automatically"
