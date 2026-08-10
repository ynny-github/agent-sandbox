package broker_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
)

func TestRequestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	want := broker.Request{
		Argv: []string{"go", "build", "./..."},
		Cwd:  "/work/project",
		Env:  []string{"AWS_PROFILE=dev"},
	}
	if err := broker.WriteRequest(&buf, want); err != nil {
		t.Fatalf("WriteRequest() error = %v", err)
	}
	got, err := broker.ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest() error = %v", err)
	}
	if got.Cwd != want.Cwd || len(got.Argv) != 3 || got.Argv[2] != "./..." || len(got.Env) != 1 {
		t.Errorf("ReadRequest() = %+v, want %+v", got, want)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := broker.WriteFrame(&buf, broker.ChanStdout, []byte("hello")); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	if err := broker.WriteFrame(&buf, broker.ChanStderr, []byte("oops")); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	if err := broker.WriteExit(&buf, 42); err != nil {
		t.Fatalf("WriteExit() error = %v", err)
	}

	f1, err := broker.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if f1.Channel != broker.ChanStdout || string(f1.Payload) != "hello" {
		t.Errorf("frame 1 = %+v, want stdout/hello", f1)
	}

	f2, err := broker.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if f2.Channel != broker.ChanStderr || string(f2.Payload) != "oops" {
		t.Errorf("frame 2 = %+v, want stderr/oops", f2)
	}

	f3, err := broker.ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if f3.Channel != broker.ChanExit || f3.ExitCode() != 42 {
		t.Errorf("frame 3 = %+v, want exit/42", f3)
	}

	if _, err := broker.ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Errorf("ReadFrame() after last frame error = %v, want io.EOF", err)
	}
}

func TestReadFrameRejectsOversizePayload(t *testing.T) {
	// channel 1, length 0xFFFFFFFF — a malformed or hostile peer.
	raw := []byte{byte(broker.ChanStdout), 0xFF, 0xFF, 0xFF, 0xFF}
	_, err := broker.ReadFrame(bytes.NewReader(raw))
	if err == nil {
		t.Fatal("ReadFrame() error = nil, want oversize rejection")
	}
}

func TestReadFrameTruncatedPayload(t *testing.T) {
	// Valid 5-byte header claiming 10 bytes of payload, but followed by EOF.
	// This is a truncated frame, not a clean end-of-stream.
	raw := []byte{byte(broker.ChanStdout), 0x00, 0x00, 0x00, 0x0A}
	_, err := broker.ReadFrame(bytes.NewReader(raw))
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
	if err := broker.WriteFrame(&buf, broker.ChanStdout, []byte("")); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	if _, err := broker.ReadFrame(&buf); err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	// Now the buffer is exhausted; the next read should encounter EOF at the header stage.
	if _, err := broker.ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Errorf("ReadFrame() clean EOF error = %v, want errors.Is(err, io.EOF)", err)
	}
}
