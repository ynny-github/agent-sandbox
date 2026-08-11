package broker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
)

// Executor runs one command. The production implementation spawns nono; tests
// substitute a fake so the server can be exercised without a sandbox.
type Executor interface {
	Execute(ctx context.Context, req Request, stdin io.Reader,
		stdout, stderr io.Writer) (int, error)
}

// Server accepts one command per connection on a unix socket. It runs in the
// launcher process, outside the sandbox, which is the whole point: a process
// inside the sandbox cannot create a new sandbox boundary.
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
		return nil, fmt.Errorf("broker: remove stale socket: %w", err)
	}
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("broker: listen on %s: %w", sockPath, err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		l.Close()
		return nil, fmt.Errorf("broker: chmod socket: %w", err)
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
	go s.watchConn(conn, stdinWriter, cancel)

	// Frames from the executor are written from two goroutines inside
	// Execute's implementation, so serialize them here.
	fw := &frameWriter{w: conn}
	code, execErr := s.exec.Execute(ctx, req, stdin,
		fw.channel(ChanStdout), fw.channel(ChanStderr))

	if stdinWriter != nil {
		stdinWriter.Close()
	}
	if execErr != nil {
		fw.writeError(execErr.Error())
		return
	}
	fw.writeExit(code)
}

// watchConn relays stdin frames into pw (nil when the request has no stdin)
// and cancels the command when the connection ends. It keeps reading after a
// stdin-close frame, because a later read error is how a disconnect is
// detected.
func (s *Server) watchConn(conn net.Conn, pw *io.PipeWriter, cancel context.CancelFunc) {
	for {
		f, err := ReadFrame(conn)
		if err != nil {
			if pw != nil {
				pw.CloseWithError(err)
			}
			cancel()
			return
		}
		switch f.Channel {
		case ChanStdin:
			if pw != nil {
				if _, werr := pw.Write(f.Payload); werr != nil {
					return
				}
			}
		case ChanStdinClose:
			if pw != nil {
				pw.Close()
				pw = nil
			}
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
