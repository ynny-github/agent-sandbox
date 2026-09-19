package execd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
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

// RunCommand sends one command line to execd and streams its output back.
func (c *Client) RunCommand(ctx context.Context, command string,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {
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

	req := Request{Command: command, Cwd: workingDir(), WithStdin: stdin != nil}
	if err := WriteRequest(conn, req); err != nil {
		return 0, err
	}

	if stdin != nil {
		go pumpStdinTo(conn, stdin)
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

func pumpStdinTo(conn net.Conn, stdin io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := stdin.Read(buf)
		if n > 0 {
			if werr := WriteFrame(conn, ChanStdin, buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			WriteFrame(conn, ChanStdinClose, nil)
			return
		}
	}
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
		stdout, stderr io.Writer) (int, error)
}

// SandboxNotRunningHint is the actionable message shown when execd is not
// reachable. It is exported because the situation is detected before any
// command runs: `agent-sandbox exec` and the MCP server both fail to build a
// client when AGENT_SANDBOX_EXECD_SOCKET is unset, and must print this rather
// than a raw dial error.
const SandboxNotRunningHint = "exec daemon is not available; run Claude via `agent-sandbox claude`, which starts it automatically"
