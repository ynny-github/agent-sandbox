// Package broker carries command execution across the sandbox boundary. The
// in-sandbox client sends a command over a unix socket; the host-side server,
// which lives in the launcher process outside the sandbox, runs it under its
// own nono sandbox and streams the output back.
//
// The wire format is one request per connection, followed by length-prefixed
// frames in both directions. Frames keep stdout and stderr separate so a
// caller can route them independently, which the router's mixed host/sandbox
// pipelines rely on.
package broker

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

// Request is the first message on a connection: what to run and where.
type Request struct {
	Argv      []string `json:"argv"`
	Cwd       string   `json:"cwd"`
	Env       []string `json:"env"`
	WithStdin bool     `json:"with_stdin"`
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
		return fmt.Errorf("broker: encode request: %w", err)
	}
	if len(data) > maxPayload {
		return fmt.Errorf("broker: request too large (%d bytes)", len(data))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("broker: write request header: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("broker: write request body: %w", err)
	}
	return nil
}

// ReadRequest reads a request written by WriteRequest.
func ReadRequest(r io.Reader) (Request, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Request{}, fmt.Errorf("broker: read request header: %w", err)
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxPayload {
		return Request{}, fmt.Errorf("broker: request too large (%d bytes)", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return Request{}, fmt.Errorf("broker: read request body: %w", err)
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return Request{}, fmt.Errorf("broker: decode request: %w", err)
	}
	return req, nil
}

// WriteFrame writes one frame: 1 byte channel, 4 bytes big-endian length, payload.
func WriteFrame(w io.Writer, ch Channel, payload []byte) error {
	if len(payload) > maxPayload {
		return fmt.Errorf("broker: payload too large (%d bytes)", len(payload))
	}
	var hdr [5]byte
	hdr[0] = byte(ch)
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("broker: write frame header: %w", err)
	}
	if len(payload) == 0 {
		return nil
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("broker: write frame payload: %w", err)
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
		return Frame{}, fmt.Errorf("broker: frame payload too large (%d bytes)", n)
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
		return Frame{}, fmt.Errorf("broker: read frame payload: %w", err)
	}
	return f, nil
}
