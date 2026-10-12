package execd

import (
	"bytes"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestTwoStageSignals_FirstRelaysSecondEscapes pins the interrupt policy
// RunCLI installs: the first signal is forwarded to the command, and the
// second abandons the connection instead. It drives the policy directly rather
// than signalling the test binary, because the real escape resets this
// process's SIGINT disposition and a test must not leave that behind.
func TestTwoStageSignals_FirstRelaysSecondEscapes(t *testing.T) {
	sigs := make(chan os.Signal, 2)
	relay := make(chan syscall.Signal, 1)
	forced := make(chan syscall.Signal, 1)
	stop := make(chan struct{})
	defer close(stop)
	var errBuf bytes.Buffer
	escaped := make(chan struct{})

	go twoStageSignals(sigs, relay, stop, &errBuf, forced, func() { close(escaped) })

	sigs <- syscall.SIGINT
	select {
	case got := <-relay:
		if got != syscall.SIGINT {
			t.Errorf("relayed %v, want SIGINT", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first signal was not relayed")
	}
	select {
	case <-escaped:
		t.Fatal("the first signal closed the connection; only the second may")
	default:
	}

	sigs <- syscall.SIGINT
	select {
	case <-escaped:
	case <-time.After(2 * time.Second):
		t.Fatal("the second signal did not close the connection")
	}
	select {
	case got := <-forced:
		if got != syscall.SIGINT {
			t.Errorf("forced signal = %v, want SIGINT", got)
		}
	default:
		t.Error("the second signal was not reported back for the exit status")
	}
	select {
	case got := <-relay:
		t.Errorf("the second signal was relayed as %v; it must close the connection instead", got)
	default:
	}
	if !strings.Contains(errBuf.String(), "second signal") {
		t.Errorf("stderr = %q, want a line explaining why the second signal differed", errBuf.String())
	}
}
