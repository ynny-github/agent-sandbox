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

// Executor runs one command line. The production implementation is
// ShellExecutor; tests substitute a fake so the server can be exercised
// without spawning anything.
type Executor interface {
	Execute(ctx context.Context, req Request, stdio Stdio) (int, error)
}

// Server accepts one command per connection on a unix socket. It runs in the
// dedicated execd process — outside the agent's own sandbox, but inside a
// nono session of its own — which is the point: a process cannot create a
// new sandbox boundary around itself, so execd needs a boundary of its
// own rather than borrowing the agent's, or worse, running with the
// unsandboxed launcher's own reach.
//
// That session is started by internal/claude.startExecd, which runs
// `nono run --profile <command profile> -- agent-sandbox execd --socket
// <path>` (see claude.ExecdArgs) as a sibling of the agent's own sandbox, not
// a child of it — nono refuses to nest.
type Server struct {
	listener net.Listener
	sockPath string
	exec     Executor

	closeOnce sync.Once
}

// NewServer creates the socket at sockPath with 0600 permissions. An existing
// stale socket at that path is removed first: a previous run that was killed
// leaves the file behind, and bind would otherwise fail forever.
func NewServer(sockPath string, exec Executor) (*Server, error) {
	if err := os.Remove(sockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("execd: remove stale socket: %w", err)
	}
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("execd: listen on %s: %w", sockPath, err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		l.Close()
		return nil, fmt.Errorf("execd: chmod socket: %w", err)
	}
	return &Server{listener: l, sockPath: sockPath, exec: exec}, nil
}

// SocketPath returns the path clients dial.
func (s *Server) SocketPath() string { return s.sockPath }

// Serve accepts connections until Close is called. It is meant to run in its
// own goroutine.
func (s *Server) Serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handle(conn)
	}
}

// Close stops accepting and removes the socket file.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.listener.Close()
		os.Remove(s.sockPath)
	})
	return err
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		WriteError(conn, "execd: not a unix connection")
		return
	}

	// The descriptors come first, before the request they belong to: a request
	// that arrives without them cannot be served at all, and receiving them
	// first is what lets that be one refusal rather than a half-started
	// request. The variable is stdio rather than io, which this file already
	// imports as a package.
	stdio, err := RecvStdio(uc)
	if err != nil {
		WriteError(conn, err.Error())
		return
	}
	// execd's own copies of the caller's three files. Closing them when the
	// request ends is the whole ownership rule: while execd holds one, a caller
	// that passed the write end of a pipe never sees EOF. This defer is
	// registered after conn's, so it runs before it — the exit frame is already
	// on the wire by then, and the caller's EOF follows it.
	defer stdio.Close()

	req, err := ReadRequest(conn)
	if err != nil {
		WriteError(conn, err.Error())
		return
	}

	if req.ProtocolVersion != ProtocolVersion {
		WriteError(conn, fmt.Sprintf(
			"execd: unsupported protocol version %d (this execd speaks %d); "+
				"the agent-sandbox binary sending this request is not the one that started execd",
			req.ProtocolVersion, ProtocolVersion))
		return
	}

	// The child must die when the client goes away. Closing the connection is
	// the client's cancellation signal, so a reader runs for every request —
	// not only those with stdin — and cancels this context on EOF.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Only this function and watchConn write frames now — the exit or error
	// that ends the request, and a refusal — so serialize the two.
	fw := &frameWriter{w: conn}

	// Buffered by one and written without blocking: a client that spams
	// signals must never be able to block the connection reader, and the
	// interesting signal in a burst is the first one, which this keeps. The
	// channel outlives the executor by design — watchConn keeps reading until
	// the connection ends, and a signal that arrives after the command is
	// gone lands in the buffer and is dropped with it.
	sigs := make(chan syscall.Signal, 1)

	go s.watchConn(conn, fw, sigs, cancel)

	// An executor that can be signalled gets the channel; one that cannot is
	// run exactly as before. The capability is reached by assertion rather
	// than by widening Executor, because Executor is the seam the tests'
	// fakes implement and a signal channel means nothing to a fake that
	// starts no process.
	var code int
	var execErr error
	if se, ok := s.exec.(interface {
		ExecuteWithSignals(context.Context, Request, Stdio, <-chan syscall.Signal) (int, error)
	}); ok {
		code, execErr = se.ExecuteWithSignals(ctx, req, stdio, sigs)
	} else {
		code, execErr = s.exec.Execute(ctx, req, stdio)
	}

	if execErr != nil {
		fw.writeError(execErr.Error())
		return
	}
	fw.writeExit(code)
}

// watchConn handles the frames a client may send — a signal, and nothing else
// — and refuses the rest. The channels are one namespace with two directions,
// and a client sending an exit frame, or stdin bytes execd no longer relays, is
// either a bug or a probe; neither should be read as data.
//
// It is also this request's disconnect detector, and that is why it reads for
// the whole life of the request rather than only while a signal might arrive.
// The client hands its descriptors over and then holds nothing but this
// connection, so the connection ending is the only thing left that says the
// caller is gone — and ending the request is the same teardown whatever
// triggered it: cancel the context, and the interpreter, the Job and execd's
// own copies of the caller's files all come down with it.
func (s *Server) watchConn(conn net.Conn, fw *frameWriter,
	sigs chan<- syscall.Signal, cancel context.CancelFunc) {
	for {
		f, err := ReadFrame(conn)
		if err != nil {
			cancel()
			return
		}
		switch f.Channel {
		case ChanSignal:
			// The allow-list is enforced here, at the boundary, and not only in
			// the client's WriteSignal: a client that does not use this package
			// — or one inside the sandbox that was tampered with — reaches this
			// code with whatever byte it likes. Frame.Signal is the one decoder,
			// so a malformed payload and a signal outside the set are refused by
			// the same rule that WriteSignal applies on the way out.
			sig, ok := f.Signal()
			if !ok {
				// This message is read inside the sandbox, by someone who
				// cannot see this side at all, so it says which signal was
				// refused rather than printing the raw bytes at them.
				msg := "execd: signal frame refused: malformed payload " +
					"(a signal frame carries exactly one byte)"
				if len(f.Payload) == 1 {
					msg = fmt.Sprintf(
						"execd: signal frame refused: signal %d may not be delivered",
						f.Payload[0])
				}
				fw.writeError(msg)
				cancel()
				return
			}
			select {
			case sigs <- sig:
			default: // a burst of signals is not worth queueing
			}
		default:
			fw.writeError(fmt.Sprintf(
				"execd: channel %d may not be sent by a client", f.Channel))
			cancel()
			return
		}
	}
}

// frameWriter serializes the frames this side writes onto one connection.
// Since stdio became descriptors the only frames left are terminal ones — an
// exit, an error, a refusal — but they still come from two goroutines, handle
// and watchConn, and a partially written frame would desync the peer's read of
// the whole stream.
type frameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (f *frameWriter) writeExit(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	WriteExit(f.w, code)
}

func (f *frameWriter) writeError(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	WriteError(f.w, msg)
}
