package execd_test

import (
	"bytes"
	"errors"
	"io"
	"syscall"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/execd"
)

func TestRequestRoundTripsACommandLine(t *testing.T) {
	var buf bytes.Buffer
	want := execd.Request{Command: "rg -n foo . | head -5", Cwd: "/w", WithStdin: true}
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
	if err := execd.WriteFrame(&buf, execd.ChanStdout, []byte("hello")); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	if err := execd.WriteFrame(&buf, execd.ChanStderr, []byte("oops")); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	if err := execd.WriteExit(&buf, 42); err != nil {
		t.Fatalf("WriteExit() error = %v", err)
	}

	f1, err := execd.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if f1.Channel != execd.ChanStdout || string(f1.Payload) != "hello" {
		t.Errorf("frame 1 = %+v, want stdout/hello", f1)
	}

	f2, err := execd.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if f2.Channel != execd.ChanStderr || string(f2.Payload) != "oops" {
		t.Errorf("frame 2 = %+v, want stderr/oops", f2)
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
	// channel 1, length 0xFFFFFFFF — a malformed or hostile peer.
	raw := []byte{byte(execd.ChanStdout), 0xFF, 0xFF, 0xFF, 0xFF}
	_, err := execd.ReadFrame(bytes.NewReader(raw))
	if err == nil {
		t.Fatal("ReadFrame() error = nil, want oversize rejection")
	}
}

func TestReadFrameTruncatedPayload(t *testing.T) {
	// Valid 5-byte header claiming 10 bytes of payload, but followed by EOF.
	// This is a truncated frame, not a clean end-of-stream.
	raw := []byte{byte(execd.ChanStdout), 0x00, 0x00, 0x00, 0x0A}
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
	if err := execd.WriteFrame(&buf, execd.ChanStdout, []byte("")); err != nil {
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

func TestClientSetsProtocolVersion(t *testing.T) {
	var buf bytes.Buffer
	if err := execd.WriteRequest(&buf, execd.Request{
		Command:         "true",
		Cwd:             "/tmp",
		ProtocolVersion: execd.ProtocolVersion,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := execd.ReadRequest(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProtocolVersion != execd.ProtocolVersion {
		t.Errorf("ProtocolVersion = %d, want %d", got.ProtocolVersion, execd.ProtocolVersion)
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
