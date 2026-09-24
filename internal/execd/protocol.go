// Package execd carries command execution across the sandbox boundary. The
// in-sandbox client sends a command line over a unix socket, along with its
// own stdin, stdout and stderr descriptors; the server, which runs outside
// the agent's own sandbox, interprets the shell language itself and the
// command writes through those descriptors directly.
//
// The wire format is one request per connection: a descriptor handshake, the
// request, and then length-prefixed frames in both directions for what is
// left — a signal from the client, and a terminal exit or error from the
// server.
package execd

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"syscall"
)

// Channel identifies which stream a frame belongs to.
type Channel byte

const (
	ChanExit  Channel = 4
	ChanError Channel = 5
	// ChanSignal carries a signal from the client to a running command. It is
	// the only way to interrupt a command short of dropping the connection,
	// which is a SIGKILL in effect. That is the rationale for both the
	// allow-list below and the two-stage interrupt in internal/cli/exec.go: a client
	// that wants a command to stop politely has this frame and nothing else.
	ChanSignal Channel = 7
)

// Channels 1, 2, 3 and 6 carried stdout, stderr, stdin and stdin-close before
// descriptors were passed instead. Their numbers are retired rather than
// reused: a number that meant two things across versions is a bug waiting in a
// reader's head.

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
//
// Before adding a field here, check whether a mismatch on it would be caught
// structurally. It is today only because this protocol's first move is a
// descriptor handshake an older binary can neither send nor read; that is a
// property of this protocol, not a general one. If a new field could be
// dropped silently by a peer that does not know it, bring back a version field
// with it.
type Request struct {
	Command string `json:"command"`
	// Cwd is client-controlled: it comes straight from the sandboxed agent's
	// own working directory (see Client.RunCommand's workingDir helper). It
	// reaches the interpreter's Dir option and, through it, --workdir of the
	// commands the interpreter execs, but nothing in this package bounds it to
	// any particular root — see ShellExecutor.Execute for where that bound
	// actually lives.
	Cwd string `json:"cwd"`
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
//
// # A blocked read on In cannot be interrupted
//
// Known, measured, and deliberately not repaired in code. Every descriptor
// that reaches RecvStdio is in blocking mode: SendStdio takes each file's
// number with (*os.File).Fd, and Fd puts the open file description back into
// blocking mode before returning. os.NewFile on a blocking fd builds a file
// the runtime poller does not register, and SetReadDeadline on such a file
// returns "file type does not support deadline" rather than arming anything.
//
// mvdan.cc/sh cancels a blocked standard input read only through that
// deadline — interp.Runner.readLine arms it from the context — and
// interp.StdIO's own doc warns about exactly this, right after the paragraph
// on passing an *os.File: an os.Pipe "has the best chance to support
// cancellable reads". A descriptor that arrived over SCM_RIGHTS is not one.
//
// So a command parked in a read on In returns when that read returns and at
// no other time: not when the request's TimeoutMs fires, not when the client
// drops the connection. runner.Run does not return, so ShellExecutor.Run does
// not, so the server's handle does not — the handler goroutine, the
// connection and all three of these descriptors are held until the byte or
// the EOF arrives. Measured 2026-09-21 through the real client: `read x` with
// TimeoutMs 700 against a pipe nobody writes to had not returned after 4s,
// and returned ExitTimeout the instant the write end was closed.
// TestPassedStdinCannotBeInterruptedWhileBlockedOnARead pins that shape.
//
// Who is exposed. Anything whose stdin is a terminal or a live producer: a
// human running `agent-sandbox exec`, and `producer | agent-sandbox exec …`.
// The agent's hook path is not — the harness gives it /dev/null, which reads
// EOF at once — which is why this is a sharp edge rather than an outage.
//
// Why no fix here. Clearing O_NONBLOCK's absence on the received descriptor
// would mutate the open file description the client and every child share,
// which is not execd's to mutate. Giving the interpreter a pipe of execd's
// own while children keep the real descriptor would break, for stdin only,
// the identity rule wiring.passthrough is built on. Both are design decisions
// about what this protocol passes, not repairs to this code.
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
	_, _, err := uc.WriteMsgUnix([]byte{stdioHandshake}, syscall.UnixRights(fds...), nil)
	// The fds above are bare integers by the time the syscall runs, so nothing
	// in the call keeps the *os.File values reachable. Every caller today holds
	// its own references for longer than this, but the doc above asks callers
	// for real files, not for their lifetime — so pin them here rather than
	// depend on that, since a finalizer closing one mid-sendmsg would send a
	// descriptor number that is no longer the caller's file.
	runtime.KeepAlive(fs)
	if err != nil {
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

	// Decode first, validate second. The kernel installs SCM_RIGHTS descriptors
	// into this process's fd table during recvmsg itself, before this function
	// sees a byte of the message: by the time control reaches here they are
	// already open, whatever the message turns out to say. Decoding does not
	// obtain them — it only recovers their numbers, which is what closing them
	// requires. So rejecting the handshake before decoding would abandon
	// descriptors this process holds and can no longer name, and a peer inside
	// the sandbox could exhaust execd's fd table by looping connect, send one
	// wrong byte with three descriptors attached, disconnect. Every rejection
	// below therefore closes what arrived.
	//
	// What remains unrecoverable is a control buffer this function cannot parse
	// at all: ParseSocketControlMessage is all-or-nothing, so a failure there
	// leaves nothing nameable to close. That buffer is written by the kernel
	// rather than by the peer — a sender controls how many descriptors it
	// attaches, not the cmsghdr layout they arrive in — and sendmsg validates
	// and installs atomically, so a send carrying a malformed header fails
	// outright rather than delivering one.
	var fds []int
	// Deliberately does not clear fds: every caller returns immediately after,
	// and the count is still wanted for the message one of them builds.
	closeAll := func() {
		for _, fd := range fds {
			syscall.Close(fd)
		}
	}
	scms, parseErr := syscall.ParseSocketControlMessage(oob[:oobn])
	// A peer inside the sandbox decides how many control messages to attach, so
	// do not assume a single one. A message that is not SCM_RIGHTS does not end
	// the loop: the messages are independent, and a later one can still carry
	// real, installed descriptors that only this loop can name. The first
	// failure is what gets reported, which is the one the old
	// return-on-first-error form reported too.
	var decodeErr error
	for i := range scms {
		got, gerr := syscall.ParseUnixRights(&scms[i])
		if gerr != nil {
			if decodeErr == nil {
				decodeErr = gerr
			}
			continue
		}
		fds = append(fds, got...)
	}

	// Decoding moved ahead of validation; reporting did not. These four checks
	// are in the order the old code applied them, so every input still produces
	// the message it always produced — a wrong handshake byte says "restart the
	// session" even when the control buffer is also bad, rather than reporting a
	// corrupt message and sending an operator after the wrong cause. What
	// changed is only that each of them now closes what arrived first.
	if n != 1 || buf[0] != stdioHandshake {
		closeAll()
		return Stdio{}, noFDs
	}
	if oobn == 0 || parseErr != nil || len(scms) == 0 {
		closeAll()
		return Stdio{}, noFDs
	}
	if decodeErr != nil {
		closeAll()
		return Stdio{}, fmt.Errorf("execd: decode stdio: %w", decodeErr)
	}
	if len(fds) != 3 {
		closeAll()
		return Stdio{}, fmt.Errorf("execd: %d descriptors arrived, want exactly 3 (stdin, stdout, stderr)", len(fds))
	}
	return Stdio{
		In:  os.NewFile(uintptr(fds[0]), "stdin"),
		Out: os.NewFile(uintptr(fds[1]), "stdout"),
		Err: os.NewFile(uintptr(fds[2]), "stderr"),
	}, nil
}
