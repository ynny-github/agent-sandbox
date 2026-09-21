package execd_test

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/execd"
)

func TestRequestRoundTripsACommandLine(t *testing.T) {
	var buf bytes.Buffer
	want := execd.Request{Command: "rg -n foo . | head -5", Cwd: "/w"}
	if err := execd.WriteRequest(&buf, want); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	got, err := execd.ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := execd.WriteFrame(&buf, execd.ChanError, []byte("hello")); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	if err := execd.WriteFrame(&buf, execd.ChanSignal, []byte("oops")); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	if err := execd.WriteExit(&buf, 42); err != nil {
		t.Fatalf("WriteExit() error = %v", err)
	}

	f1, err := execd.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if f1.Channel != execd.ChanError || string(f1.Payload) != "hello" {
		t.Errorf("frame 1 = %+v, want error/hello", f1)
	}

	f2, err := execd.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if f2.Channel != execd.ChanSignal || string(f2.Payload) != "oops" {
		t.Errorf("frame 2 = %+v, want signal/oops", f2)
	}

	f3, err := execd.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if f3.Channel != execd.ChanExit || f3.ExitCode() != 42 {
		t.Errorf("frame 3 = %+v, want exit/42", f3)
	}

	if _, err := execd.ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Errorf("ReadFrame() after last frame error = %v, want io.EOF", err)
	}
}

func TestReadFrameRejectsOversizePayload(t *testing.T) {
	// channel 5, length 0xFFFFFFFF — a malformed or hostile peer.
	raw := []byte{byte(execd.ChanError), 0xFF, 0xFF, 0xFF, 0xFF}
	_, err := execd.ReadFrame(bytes.NewReader(raw))
	if err == nil {
		t.Fatal("ReadFrame() error = nil, want oversize rejection")
	}
}

func TestReadFrameTruncatedPayload(t *testing.T) {
	// Valid 5-byte header claiming 10 bytes of payload, but followed by EOF.
	// This is a truncated frame, not a clean end-of-stream.
	raw := []byte{byte(execd.ChanError), 0x00, 0x00, 0x00, 0x0A}
	_, err := execd.ReadFrame(bytes.NewReader(raw))
	if err == nil {
		t.Fatal("ReadFrame() error = nil, want truncation error")
	}
	// A truncated frame must NOT satisfy errors.Is(err, io.EOF).
	if errors.Is(err, io.EOF) {
		t.Errorf("ReadFrame() truncated frame error = %v, should NOT satisfy errors.Is(err, io.EOF)", err)
	}
	// Verify we do still get clean EOF at a frame boundary: write a complete zero-payload
	// frame and then try to read past it.
	var buf bytes.Buffer
	if err := execd.WriteFrame(&buf, execd.ChanError, []byte("")); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	if _, err := execd.ReadFrame(&buf); err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	// Now the buffer is exhausted; the next read should encounter EOF at the header stage.
	if _, err := execd.ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Errorf("ReadFrame() clean EOF error = %v, want errors.Is(err, io.EOF)", err)
	}
}

func TestSignalFrameRoundTrip(t *testing.T) {
	for _, sig := range []syscall.Signal{
		syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGKILL,
	} {
		var buf bytes.Buffer
		if err := execd.WriteSignal(&buf, sig); err != nil {
			t.Fatalf("WriteSignal(%v): %v", sig, err)
		}
		f, err := execd.ReadFrame(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if f.Channel != execd.ChanSignal {
			t.Fatalf("channel = %d, want ChanSignal", f.Channel)
		}
		got, ok := f.Signal()
		if !ok || got != sig {
			t.Errorf("Signal() = %v, %v; want %v, true", got, ok, sig)
		}
	}
}

func TestSignalFrameRejectsOtherSignals(t *testing.T) {
	if err := execd.WriteSignal(io.Discard, syscall.SIGUSR1); err == nil {
		t.Error("WriteSignal(SIGUSR1) = nil, want an error")
	}
	f := execd.Frame{Channel: execd.ChanSignal, Payload: []byte{byte(syscall.SIGUSR1)}}
	if _, ok := f.Signal(); ok {
		t.Error("Signal() accepted SIGUSR1; want it refused")
	}
}

// socketPair returns the two ends of a connected unix stream socket, which is
// what lets these tests exercise descriptor passing without a listener.
func socketPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := func(fd int) *net.UnixConn {
		f := os.NewFile(uintptr(fd), "socketpair")
		defer f.Close()
		c, err := net.FileConn(f)
		if err != nil {
			t.Fatal(err)
		}
		uc, ok := c.(*net.UnixConn)
		if !ok {
			t.Fatalf("FileConn returned %T, want *net.UnixConn", c)
		}
		t.Cleanup(func() { uc.Close() })
		return uc
	}
	return conn(fds[0]), conn(fds[1])
}

func TestStdioRoundTripsOverASocket(t *testing.T) {
	a, b := socketPair(t)
	path := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()

	// Buffered so the goroutine never blocks handing back its result, and
	// checked below: silently discarding a SendStdio failure would leave
	// RecvStdio blocked in ReadMsgUnix with nothing telling us why, which is
	// the worst failure mode a test can have.
	sendErr := make(chan error, 1)
	go func() { sendErr <- execd.SendStdio(a, execd.Stdio{In: devnull, Out: f, Err: f}) }()

	got, err := execd.RecvStdio(b)
	if err != nil {
		t.Fatalf("RecvStdio: %v", err)
	}
	defer got.Close()
	if err := <-sendErr; err != nil {
		t.Fatalf("SendStdio: %v", err)
	}
	// The received files are different descriptors for the same open files, so
	// identity cannot be asserted — what must hold is that writing through the
	// received end reaches the same file.
	if _, err := got.Out.WriteString("through the passed descriptor\n"); err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b2), "through the passed descriptor") {
		t.Errorf("file = %q, want the bytes written through the received descriptor", b2)
	}
}

func TestRecvStdioRefusesARequestWithNoDescriptors(t *testing.T) {
	a, b := socketPair(t)
	// A peer that speaks the old protocol writes the request straight away,
	// with no control message. Buffered and checked below for the same reason
	// as TestStdioRoundTripsOverASocket: a silently discarded write failure
	// here would otherwise leave RecvStdio's failure indistinguishable from a
	// hang.
	sendErr := make(chan error, 1)
	go func() { sendErr <- execd.WriteRequest(a, execd.Request{Command: "true", Cwd: "/tmp"}) }()

	_, err := execd.RecvStdio(b)
	if err == nil {
		t.Fatal("RecvStdio accepted a message carrying no descriptors")
	}
	if !strings.Contains(err.Error(), "restart the session") {
		t.Errorf("error = %q, want it to name the likely cause and the remedy", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
}

func TestSendStdioRefusesANilFile(t *testing.T) {
	a, _ := socketPair(t)
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := execd.SendStdio(a, execd.Stdio{In: nil, Out: f, Err: f}); err == nil {
		t.Error("SendStdio accepted a nil file; the caller must pass a real one")
	}
}

// TestStdioCloseIsSafeOnAZeroOrPartialValue exercises the shape a failed
// RecvStdio hands back: a zero Stdio, or (in principle) one where only some
// fields ended up populated. Close must not panic on either.
func TestStdioCloseIsSafeOnAZeroOrPartialValue(t *testing.T) {
	execd.Stdio{}.Close()

	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	execd.Stdio{Out: f}.Close()
}

// openFDCount reports how many descriptors this process currently has open,
// for leak detection around RecvStdio's error paths. It skips on platforms
// without /proc rather than failing, since it's a diagnostic aid, not the
// behaviour under test.
func openFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd on this platform; skipping leak check")
	}
	return len(entries)
}

// TestRecvStdioClosesDescriptorsOnWrongCount automates the empirical check
// the reviewer ran by hand against a71fbcb (sending 4 and 20 descriptors into
// the 3-slot handshake and confirming none leaked): a peer that hands over
// the wrong number of descriptors must not cost this process any descriptors
// once RecvStdio has returned its error.
func TestRecvStdioClosesDescriptorsOnWrongCount(t *testing.T) {
	a, b := socketPair(t)
	d1, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer d1.Close()
	d2, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()

	// Snapshot after setup (the socket pair and the sender's own two files
	// are all open on this side already) so the only thing the before/after
	// delta can attribute to RecvStdio is the receiver-side descriptors the
	// kernel installs for the control message below.
	before := openFDCount(t)

	sendErr := make(chan error, 1)
	go func() {
		_, _, err := a.WriteMsgUnix([]byte{1}, syscall.UnixRights(int(d1.Fd()), int(d2.Fd())), nil)
		sendErr <- err
	}()

	_, err = execd.RecvStdio(b)
	if err == nil {
		t.Fatal("RecvStdio accepted a request with the wrong descriptor count")
	}
	if !strings.Contains(err.Error(), "2 descriptors arrived") {
		t.Errorf("error = %q, want it to name the count that arrived", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("WriteMsgUnix: %v", err)
	}

	if after := openFDCount(t); after != before {
		t.Errorf("open descriptor count = %d after RecvStdio's error, want %d (no leak)", after, before)
	}
}

// TestRecvStdioClosesDescriptorsOnAWrongHandshakeByte covers the rejection
// that is reachable on purpose. The kernel installs SCM_RIGHTS descriptors
// during recvmsg, before RecvStdio inspects anything, so a peer that attaches
// three descriptors to a message whose first byte is not the handshake has
// already cost this process three open descriptors by the time the byte is
// looked at. Returning on the byte before decoding the control message would
// abandon them unnamed, and a client inside the sandbox can loop connect,
// send, disconnect until execd's fd table is exhausted and every request after
// that fails.
//
// This is why RecvStdio decodes first and validates second. The wrong-count
// path is covered above; this is the path a hostile peer would actually take,
// since it costs it nothing to get the byte wrong.
func TestRecvStdioClosesDescriptorsOnAWrongHandshakeByte(t *testing.T) {
	a, b := socketPair(t)
	var held []*os.File
	for i := 0; i < 3; i++ {
		f, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		held = append(held, f)
	}

	// Snapshot after setup, so the only delta attributable to RecvStdio is the
	// receiver-side descriptors the kernel installs for the control message.
	before := openFDCount(t)

	sendErr := make(chan error, 1)
	go func() {
		fds := make([]int, 0, len(held))
		for _, f := range held {
			fds = append(fds, int(f.Fd()))
		}
		// A well-formed control message carrying exactly the right number of
		// descriptors, attached to the wrong byte: everything about this
		// message is acceptable except the one thing RecvStdio checks first.
		_, _, err := a.WriteMsgUnix([]byte{0x00}, syscall.UnixRights(fds...), nil)
		sendErr <- err
	}()

	_, err := execd.RecvStdio(b)
	if err == nil {
		t.Fatal("RecvStdio accepted a message whose handshake byte was wrong")
	}
	if !strings.Contains(err.Error(), "restart the session") {
		t.Errorf("error = %q, want it to name the likely cause and the remedy", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("WriteMsgUnix: %v", err)
	}

	if after := openFDCount(t); after != before {
		t.Errorf("open descriptor count = %d after RecvStdio's error, want %d: "+
			"the descriptors the kernel installed for a refused message were leaked",
			after, before)
	}
}
