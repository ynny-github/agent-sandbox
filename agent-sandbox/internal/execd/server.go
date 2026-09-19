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
	Execute(ctx context.Context, req Request, stdin io.Reader,
		stdout, stderr io.Writer) (int, error)
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

	var stdin io.Reader
	var stdinWriter *io.PipeWriter
	if req.WithStdin {
		pr, pw := io.Pipe()
		stdin, stdinWriter = pr, pw
	}
	// Frames from the executor are written from two goroutines inside
	// Execute's implementation, so serialize them here.
	fw := &frameWriter{w: conn}

	// Buffered by one and written without blocking: a client that spams
	// signals must never be able to block the connection reader, and the
	// interesting signal in a burst is the first one, which this keeps. The
	// channel outlives the executor by design — watchConn keeps reading until
	// the connection ends, and a signal that arrives after the command is
	// gone lands in the buffer and is dropped with it.
	sigs := make(chan syscall.Signal, 1)

	go s.watchConn(conn, fw, stdinWriter, sigs, cancel)

	// An executor that can be signalled gets the channel; one that cannot is
	// run exactly as before. The capability is reached by assertion rather
	// than by widening Executor, because Executor is the seam the tests'
	// fakes implement and a signal channel means nothing to a fake that
	// starts no process.
	var code int
	var execErr error
	if se, ok := s.exec.(interface {
		ExecuteWithSignals(context.Context, Request, io.Reader, io.Writer, io.Writer,
			<-chan syscall.Signal) (int, error)
	}); ok {
		code, execErr = se.ExecuteWithSignals(ctx, req, stdin,
			fw.channel(ChanStdout), fw.channel(ChanStderr), sigs)
	} else {
		code, execErr = s.exec.Execute(ctx, req, stdin,
			fw.channel(ChanStdout), fw.channel(ChanStderr))
	}

	if stdinWriter != nil {
		stdinWriter.Close()
	}
	if execErr != nil {
		fw.writeError(execErr.Error())
		return
	}
	fw.writeExit(code)
}

// watchConn relays the frames a client may send — stdin, stdin-close, signal —
// and refuses the rest. The channels are one namespace with two directions, and
// a client sending an exit frame is either a bug or a probe; neither should be
// read as data.
//
// It relays stdin frames into pw (nil when the request has no stdin) and
// cancels the command when the connection ends. It keeps reading after a
// stdin-close frame, because a later read error is how a disconnect is
// detected.
func (s *Server) watchConn(conn net.Conn, fw *frameWriter, pw *io.PipeWriter,
	sigs chan<- syscall.Signal, cancel context.CancelFunc) {
	// end tears this request down the same way whatever ended it: the stdin
	// pipe is failed, not merely abandoned, so a reader blocked on it wakes
	// with an error instead of depending on the executor noticing the
	// cancellation. The read-error path already did this; the refusal paths
	// only cancelled, which was correct solely because ShellExecutor honours
	// the context. Nothing writes to pw after this returns, so failing it is
	// the honest description of the state either way.
	end := func(err error) {
		if pw != nil {
			pw.CloseWithError(err)
			pw = nil
		}
		cancel()
	}
	for {
		f, err := ReadFrame(conn)
		if err != nil {
			end(err)
			return
		}
		switch f.Channel {
		case ChanStdin:
			if pw != nil {
				if _, werr := pw.Write(f.Payload); werr != nil {
					// The one path that does not end the request. A failed
					// write means the command stopped reading its stdin — it
					// exited, or never read at all — which says nothing about
					// whether the request is over or the client is still
					// there. So stop feeding the pipe, exactly as end does,
					// and keep reading the connection: this loop is now the
					// only route for a signal frame and the only detector of
					// a disconnect, and returning here would silently cost
					// the request both for the rest of its life.
					pw.CloseWithError(werr)
					pw = nil
				}
			}
		case ChanStdinClose:
			if pw != nil {
				pw.Close()
				pw = nil
			}
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
				end(errors.New(msg))
				return
			}
			select {
			case sigs <- sig:
			default: // a burst of signals is not worth queueing
			}
		default:
			fw.writeError(fmt.Sprintf(
				"execd: channel %d may not be sent by a client", f.Channel))
			end(fmt.Errorf("execd: channel %d may not be sent by a client", f.Channel))
			return
		}
	}
}

// frameWriter serializes concurrent frame writes onto one connection.
type frameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (f *frameWriter) channel(ch Channel) io.Writer {
	return channelWriter{fw: f, ch: ch}
}

func (f *frameWriter) write(ch Channel, p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := WriteFrame(f.w, ch, p); err != nil {
		return 0, err
	}
	return len(p), nil
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

type channelWriter struct {
	fw *frameWriter
	ch Channel
}

func (c channelWriter) Write(p []byte) (int, error) { return c.fw.write(c.ch, p) }
