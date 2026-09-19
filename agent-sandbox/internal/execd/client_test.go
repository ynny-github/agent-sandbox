package execd_test

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/execd"
)

func TestExecWiresStdin(t *testing.T) {
	sock := startTestServer(t, execd.NewShellExecutor())
	c := execd.NewClient(sock)
	var out strings.Builder
	code, err := c.RunCommand(context.Background(), "cat",
		strings.NewReader("fed-through\n"), &out, io.Discard, execd.RunOptions{})
	if err != nil || code != 0 {
		t.Fatalf("RunCommand = %d, %v", code, err)
	}
	if strings.TrimSpace(out.String()) != "fed-through" {
		t.Errorf("out = %q, want \"fed-through\"", out.String())
	}
}

// A wiring regression here — the timeout never reaching the request — must
// fail fast rather than only be caught once "sleep 30" finishes on its own,
// which is why this asserts the elapsed time is bounded well under 30s and
// not just the exit code.
func TestExecForwardsTimeout(t *testing.T) {
	sock := startTestServer(t, execd.NewShellExecutor())
	c := execd.NewClient(sock)
	start := time.Now()
	code, err := c.RunCommand(context.Background(), "sleep 30", nil, io.Discard, io.Discard,
		execd.RunOptions{TimeoutMs: 400})
	if err != nil {
		t.Fatal(err)
	}
	if code != 124 {
		t.Errorf("exit = %d, want 124", code)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v; want it bounded by TimeoutMs", d)
	}
}

// trickleStdin feeds the command one byte at a time, with a short pause
// between reads, so the client's stdin pump goroutine keeps writing ChanStdin
// frames on the connection for as long as stop stays open. That is the
// window TestExecSignalDuringActiveStdin needs: a signal sent from
// RunOptions.Signals while stdin is actively streaming is exactly the
// scenario where an unserialized client would interleave a signal frame into
// the middle of a stdin frame and desync the connection.
type trickleStdin struct {
	stop <-chan struct{}
}

func (s *trickleStdin) Read(p []byte) (int, error) {
	select {
	case <-s.stop:
		return 0, io.EOF
	case <-time.After(time.Millisecond):
		p[0] = 'x'
		return 1, nil
	}
}

// readyTrigger watches stdout for a "ready" marker and fires fire() exactly
// once as soon as it appears, so a signal can be sent the moment the
// command's trap is known to be armed rather than on a timer.
type readyTrigger struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	once sync.Once
	fire func()
}

func (r *readyTrigger) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.buf.Write(p)
	ready := strings.Contains(r.buf.String(), "ready")
	r.mu.Unlock()
	if ready {
		r.once.Do(r.fire)
	}
	return len(p), nil
}

// TestExecSignalDuringActiveStdin is the missing end-to-end case: a signal
// sent through RunOptions.Signals while stdin is actively being pumped on
// the same connection. Before the client serialized its frame writes
// (connWriter in client.go), the stdin pump and the signal forwarder wrote
// to the connection from separate goroutines with no coordination, and a
// signal frame could land in the middle of a stdin frame's header/payload
// writes, corrupting the stream the server reads. The command can only
// report exit 42 by actually trapping the signal, so a regression here shows
// up as a wrong exit code, a RunCommand error, or (bounded by ctx) a hang.
func TestExecSignalDuringActiveStdin(t *testing.T) {
	// Distinctive marker so cleanup cannot match an unrelated process; belt
	// and suspenders alongside the ctx timeout below, which already causes
	// execd to kill the command's process group when the connection closes.
	t.Cleanup(func() { exec.Command("pkill", "-f", "execd-signal-stdin-probe").Run() })

	sock := startTestServer(t, execd.NewShellExecutor())
	c := execd.NewClient(sock)

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	relay := make(chan syscall.Signal, 1)
	out := &readyTrigger{fire: func() { relay <- syscall.SIGTERM }}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The backgrounded cat reads the same stdin the shell was given, so it is
	// what keeps consuming the trickle; `& wait` (rather than a foreground
	// cat) is what lets the trap fire before stdin ever closes, the same
	// technique TestServerForwardsSignalToTheCommand uses with sleep. The
	// leading no-op ": execd-signal-stdin-probe" puts a distinctive, harmless
	// token into the process's own argv so cleanup's pkill -f can find it.
	command := `sh -c ': execd-signal-stdin-probe; trap "exit 42" TERM; echo ready; ` +
		`cat >/dev/null & wait'`

	start := time.Now()
	code, err := c.RunCommand(ctx, command, &trickleStdin{stop: stop}, out, io.Discard,
		execd.RunOptions{Signals: relay})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if code != 42 {
		t.Errorf("exit = %d, want 42 from the trap (stdout=%q)", code, out.buf.String())
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v; a corrupted stream should fail fast via ctx, not hang", d)
	}
}
