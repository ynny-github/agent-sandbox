// Package execd carries command execution across the sandbox boundary. The
// in-sandbox client sends a command line over a unix socket; the server, which
// runs outside the agent's own sandbox, interprets the shell language itself
// and streams the output back.
//
// The wire format is one request per connection, followed by length-prefixed
// frames in both directions. Frames keep stdout and stderr separate so a
// caller can route them independently.
package execd

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
)

// Channel identifies which stream a frame belongs to.
type Channel byte

const (
	ChanStdout Channel = 1
	ChanStderr Channel = 2
	ChanStdin  Channel = 3
	ChanExit   Channel = 4
	ChanError  Channel = 5
	// ChanStdinClose signals end-of-input; a zero-length stdin frame would be
	// ambiguous with "no bytes available yet".
	ChanStdinClose Channel = 6
	// ChanSignal carries a signal from the client to a running command. It is the
	// only way to interrupt a command short of dropping the connection, which is a
	// SIGKILL in effect.
	ChanSignal Channel = 7
)

// deliverableSignals is the set a client may send. It is an allow-list rather
// than a range check because the number travels across a sandbox boundary: a
// caller inside the sandbox must not be able to name a signal the profile
// author never considered.
var deliverableSignals = map[syscall.Signal]bool{
	syscall.SIGINT:  true,
	syscall.SIGTERM: true,
	syscall.SIGHUP:  true,
	syscall.SIGQUIT: true,
	syscall.SIGKILL: true,
}

// WriteSignal writes a signal frame. It refuses a signal outside the
// deliverable set rather than sending a frame the peer will reject.
func WriteSignal(w io.Writer, sig syscall.Signal) error {
	if !deliverableSignals[sig] {
		return fmt.Errorf("execd: signal %d is not deliverable", sig)
	}
	return WriteFrame(w, ChanSignal, []byte{byte(sig)})
}

// Signal decodes a signal frame. The second result is false for a payload that
// is not exactly one deliverable signal number.
func (f Frame) Signal() (syscall.Signal, bool) {
	if f.Channel != ChanSignal || len(f.Payload) != 1 {
		return 0, false
	}
	sig := syscall.Signal(f.Payload[0])
	return sig, deliverableSignals[sig]
}

// maxPayload bounds a single frame so a malformed length cannot make the peer
// allocate without limit. 1 MiB comfortably exceeds any realistic pipe read.
const maxPayload = 1 << 20

// ProtocolVersion is the wire contract this binary speaks. The server refuses
// any other value rather than serving it on a best effort, because
// encoding/json drops unknown fields: without this check an execd that predates
// a request field would ignore it silently — a command asking for a timeout
// would simply run without one. Client and server are the same binary, but the
// launcher starts execd from its own path while the sandboxed client resolves
// agent-sandbox through PATH, so a mismatched pair is reachable.
//
// It does not protect against an execd built before this constant existed:
// that server has no check to run. What it does is make every later mismatch
// loud.
const ProtocolVersion = 1

// Request is the first message on a connection: what to run and where.
//
// It carries a command *line*, not an argv. execd interprets the shell
// language itself and executes each simple command, so splitting it here would
// duplicate that work in the one place that cannot see the result.
//
// There is deliberately no environment field. The command's environment is a
// policy decision owned by the command profile: execd's own environment is
// filtered by nono before it starts, and each command's is decided by its entry.
// A request-supplied environment could not work — the agent's nono profile
// strips those variables long before they could be reported — and must not
// work, because the request originates inside the sandbox it would configure.
type Request struct {
	Command string `json:"command"`
	// Cwd is client-controlled: it comes straight from the sandboxed agent's
	// own working directory (see Client.RunCommand's workingDir helper). It
	// reaches the interpreter's Dir option and, through it, --workdir of the
	// commands the interpreter execs, but nothing in this package bounds it to
	// any particular root — see ShellExecutor.Execute for where that bound
	// actually lives.
	Cwd             string `json:"cwd"`
	WithStdin       bool   `json:"with_stdin"`
	ProtocolVersion int    `json:"protocol_version"`
	// TimeoutMs bounds the whole request. Zero means no bound: the caller's own
	// timeout (the harness's, for an agent's command) is the only one.
	TimeoutMs int `json:"timeout_ms"`
}

// Frame is one decoded frame.
type Frame struct {
	Channel Channel
	Payload []byte
}

// ExitCode decodes an exit frame's payload. It is meaningless on other channels.
func (f Frame) ExitCode() int {
	if len(f.Payload) != 4 {
		return -1
	}
	return int(int32(binary.BigEndian.Uint32(f.Payload)))
}

// WriteRequest writes the JSON request with a 4-byte big-endian length prefix.
func WriteRequest(w io.Writer, req Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("execd: encode request: %w", err)
	}
	if len(data) > maxPayload {
		return fmt.Errorf("execd: request too large (%d bytes)", len(data))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("execd: write request header: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("execd: write request body: %w", err)
	}
	return nil
}

// ReadRequest reads a request written by WriteRequest.
func ReadRequest(r io.Reader) (Request, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Request{}, fmt.Errorf("execd: read request header: %w", err)
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxPayload {
		return Request{}, fmt.Errorf("execd: request too large (%d bytes)", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return Request{}, fmt.Errorf("execd: read request body: %w", err)
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return Request{}, fmt.Errorf("execd: decode request: %w", err)
	}
	return req, nil
}

// WriteFrame writes one frame: 1 byte channel, 4 bytes big-endian length, payload.
func WriteFrame(w io.Writer, ch Channel, payload []byte) error {
	if len(payload) > maxPayload {
		return fmt.Errorf("execd: payload too large (%d bytes)", len(payload))
	}
	var hdr [5]byte
	hdr[0] = byte(ch)
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("execd: write frame header: %w", err)
	}
	if len(payload) == 0 {
		return nil
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("execd: write frame payload: %w", err)
	}
	return nil
}

// WriteExit writes the terminal exit frame.
func WriteExit(w io.Writer, code int) error {
	var payload [4]byte
	binary.BigEndian.PutUint32(payload[:], uint32(int32(code)))
	return WriteFrame(w, ChanExit, payload[:])
}

// WriteError writes a terminal error frame carrying a human-readable message.
func WriteError(w io.Writer, msg string) error {
	return WriteFrame(w, ChanError, []byte(msg))
}

// ReadFrame reads one frame. It returns an error wrapping io.EOF when the
// stream ends cleanly at a frame boundary.
func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxPayload {
		return Frame{}, fmt.Errorf("execd: frame payload too large (%d bytes)", n)
	}
	f := Frame{Channel: Channel(hdr[0])}
	if n == 0 {
		return f, nil
	}
	f.Payload = make([]byte, n)
	if _, err := io.ReadFull(r, f.Payload); err != nil {
		// If the payload read fails, it's a truncation, not a clean end-of-stream.
		// io.ReadFull returns plain io.EOF if zero bytes were consumed; remap it
		// so errors.Is(err, io.EOF) returns false for truncations.
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, fmt.Errorf("execd: read frame payload: %w", err)
	}
	return f, nil
}

// Stdio is the trio of descriptors a request runs on. It is passed across the
// socket rather than relayed byte by byte: the command writes to the caller's
// own files, so there is nothing to frame, nothing to drain, and no EOF for
// this side to wait on.
//
// All three are always present. A caller with no stdin opens /dev/null and
// passes that; substituting one here would put a branch in the one place the
// design wants none.
type Stdio struct {
	In, Out, Err *os.File
}

func (s Stdio) files() [3]*os.File { return [3]*os.File{s.In, s.Out, s.Err} }

// Close closes every file in the trio. The receiver owns what it received and
// must call this when the request ends: while execd holds a copy, a caller
// that passed the write end of a pipe never sees EOF.
func (s Stdio) Close() {
	for _, f := range s.files() {
		if f != nil {
			f.Close()
		}
	}
}

// stdioHandshake is the one-byte message the descriptors ride on. A control
// message is delivered with the first byte of the range it was attached to, so
// a receiver that reads exactly one byte needs no reasoning about short reads.
// Attaching the descriptors to the request's own length prefix would bring that
// reasoning back for nothing.
const stdioHandshake = 0x01

// SendStdio passes the trio to the peer. It must be called before the request:
// a request that arrives without descriptors is refused.
func SendStdio(uc *net.UnixConn, s Stdio) error {
	fs := s.files()
	fds := make([]int, 0, len(fs))
	for i, f := range fs {
		if f == nil {
			return fmt.Errorf("execd: stdio[%d] is nil; pass a real file (open %s when there is none)", i, os.DevNull)
		}
		fds = append(fds, int(f.Fd()))
	}
	if _, _, err := uc.WriteMsgUnix([]byte{stdioHandshake}, syscall.UnixRights(fds...), nil); err != nil {
		return fmt.Errorf("execd: send stdio: %w", err)
	}
	return nil
}

// RecvStdio reads the handshake and returns the three descriptors it carried.
func RecvStdio(uc *net.UnixConn) (Stdio, error) {
	buf := make([]byte, 1)
	oob := make([]byte, syscall.CmsgSpace(3*4))
	n, oobn, _, _, err := uc.ReadMsgUnix(buf, oob)
	if err != nil {
		return Stdio{}, fmt.Errorf("execd: receive stdio: %w", err)
	}
	// The message that should carry the descriptors is one fixed byte. Anything
	// else means the peer is not speaking this protocol at all — which is what
	// an older agent-sandbox binary looks like from here, since its first move
	// is the request itself.
	noFDs := fmt.Errorf("execd: no file descriptors arrived with the request. " +
		"This usually means the agent-sandbox binary sending it is older than the " +
		"execd serving it — restart the session so both come from the same build")
	if n != 1 || buf[0] != stdioHandshake || oobn == 0 {
		return Stdio{}, noFDs
	}
	// ParseSocketControlMessage is all-or-nothing: on error it discards
	// anything it already decoded, so there is never a partially-decoded
	// message this call could leak. Nothing arrived that this function can
	// identify, so there is nothing to close.
	scms, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(scms) == 0 {
		return Stdio{}, noFDs
	}
	// A peer inside the sandbox controls these bytes; do not assume a single
	// well-formed message. Decode every control message the kernel handed
	// back, and if a later one fails to decode, close whatever earlier ones
	// already gave us real, kernel-installed descriptors for — otherwise a
	// crafted trailing message would leak them.
	var fds []int
	for i := range scms {
		got, err := syscall.ParseUnixRights(&scms[i])
		if err != nil {
			for _, fd := range fds {
				syscall.Close(fd)
			}
			return Stdio{}, fmt.Errorf("execd: decode stdio: %w", err)
		}
		fds = append(fds, got...)
	}
	if len(fds) != 3 {
		for _, fd := range fds {
			syscall.Close(fd)
		}
		return Stdio{}, fmt.Errorf("execd: %d descriptors arrived, want exactly 3 (stdin, stdout, stderr)", len(fds))
	}
	return Stdio{
		In:  os.NewFile(uintptr(fds[0]), "stdin"),
		Out: os.NewFile(uintptr(fds[1]), "stdout"),
		Err: os.NewFile(uintptr(fds[2]), "stderr"),
	}, nil
}
