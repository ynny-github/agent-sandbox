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
)

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
