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

// RunCommand sends one command line to execd and returns its exit status.
//
// The command runs on stdio: those three files are passed to execd over the
// socket and the command writes through them directly, so nothing of the
// command's output travels on this connection. All three must be real files —
// a caller with no input to send opens os.DevNull — because Stdio has no
// branch for an absent one.
//
// This connection carries what is left: the request, a signal the caller
// forwards, and the exit status. It is also the request's lifeline — dropping
// it is how execd learns the caller is gone — which is why it stays open for
// the whole call even though no bytes of the command flow on it.
func (c *Client) RunCommand(ctx context.Context, command string,
	stdio Stdio, opts RunOptions) (int, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.sockPath)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrExecdUnavailable, err)
	}
	defer conn.Close()
	// Descriptors travel on a SCM_RIGHTS control message, which only a unix
	// socket carries. Dialing "unix" always yields one; this is the assertion
	// rather than the possibility.
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("execd: %s is not a unix socket connection", c.sockPath)
	}

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

	// Before the request, because execd refuses a request that arrives without
	// descriptors: sending them first is what makes a mismatched pair one
	// refusal instead of a half-started request.
	if err := SendStdio(uc, stdio); err != nil {
		return 0, err
	}

	req := Request{
		Command:         command,
		Cwd:             workingDir(),
		ProtocolVersion: ProtocolVersion,
		TimeoutMs:       opts.TimeoutMs,
	}
	if err := WriteRequest(conn, req); err != nil {
		return 0, err
	}

	cw := &connWriter{c: conn}

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
					// A write failure here usually means the connection is
					// already gone — the peer closed it, or the ctx.Done()
					// goroutine above closed it first — and the read loop
					// below observes the same failure via ReadFrame and
					// turns it into RunCommand's returned error, so there is
					// nothing further to report from this side. But
					// writeSignal can also fail before it ever touches the
					// connection: WriteSignal rejects a signal outside the
					// deliverable allow-list first (see deliverableSignals
					// in protocol.go). In that case the connection stays
					// healthy, the read loop sees nothing wrong, and the
					// signal vanishes with no diagnostic. No production
					// caller hits this today — cmd/exec.go relays only
					// SIGINT and SIGTERM, both deliverable — but a library
					// caller forwarding an arbitrary signal would lose it
					// silently.
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
		case ChanExit:
			return f.ExitCode(), nil
		case ChanError:
			return 0, fmt.Errorf("execd: %s", f.Payload)
		default:
			// The direction rule, from this side. Only an exit and an error
			// are ever sent to a client now, so anything else is a peer that
			// is not speaking this protocol — and reading it as data, or
			// silently skipping it, would leave a desynced stream to surface
			// later as something harder to read than this.
			return 0, fmt.Errorf("execd: channel %d may not be sent by the server", f.Channel)
		}
	}
}

// connWriter owns every frame this client writes. Since stdio became
// descriptors there is one writer left — signal forwarding — so the mutex is
// uncontended today. It stays because the rule it encodes is what keeps a
// second writer from interleaving a partial frame onto the wire, which is a
// defect that reads as a corrupted stream rather than as a race.
type connWriter struct {
	mu sync.Mutex
	c  net.Conn
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
	RunCommand(ctx context.Context, command string, stdio Stdio,
		opts RunOptions) (int, error)
}

// SandboxNotRunningHint is the actionable message shown when execd is not
// reachable. It is exported because the situation is detected before any
// command runs: `agent-sandbox exec` and the MCP server both fail to build a
// client when AGENT_SANDBOX_EXECD_SOCKET is unset, and must print this rather
// than a raw dial error.
const SandboxNotRunningHint = "exec daemon is not available; run Claude via `agent-sandbox claude`, which starts it automatically"
