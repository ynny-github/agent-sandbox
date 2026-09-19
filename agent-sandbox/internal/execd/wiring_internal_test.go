package execd

// This file is in package execd, not execd_test, because what it pins is
// internal: waitDrains' bound is not reachable through any exported API without
// a real nono session, and the whole point of the test is that it needs none.

import (
	"io"
	"os"
	"syscall"
	"testing"
	"time"
)

// TestWaitDrainsReturnsWhenAnOutsiderHoldsTheWriteEnd is the second of the two
// assertions the design calls for — the first being
// TestRunLeavesNoDescendants — and it covers what that one cannot. There the
// output fd is held by a grandchild inside the process group, so killpg
// releases it and the drains reach EOF on their own; waitDrains never spends
// its grace and the truncation path never runs. Here nothing will ever close
// the write end, which is the shape measured against nono: the shim keeps a
// duplicate on the far side of the boundary, no EOF arrives, and this bound is
// the only thing that ends the request. Since refusePolicyPipeChains was
// deleted it is the only thing, full stop. See DrainGrace.
//
// The duplicate is made with dup(2) rather than by simply holding pw, because
// the situation being modelled is precisely that closeJobEnds did its job and a
// copy outside this package's reach survived it anyway.
//
// It passes its own short grace to waitDrains instead of DrainGrace: the
// production constant stays a constant, since the bound is already a parameter
// of the function under test.
func TestWaitDrainsReturnsWhenAnOutsiderHoldsTheWriteEnd(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	dup, err := syscall.Dup(int(pw.Fd()))
	if err != nil {
		pr.Close()
		pw.Close()
		t.Fatalf("dup the write end: %v", err)
	}
	held := os.NewFile(uintptr(dup), "outsider-write-end")
	defer held.Close()
	// The job's own copy goes, as closeJobEnds drops it right after Start. Only
	// the outsider's duplicate is left, so the read end never sees EOF.
	if err := pw.Close(); err != nil {
		t.Fatalf("close the job's write end: %v", err)
	}

	w := &wiring{}
	d := startDrain(io.Discard, pr)
	w.drains = append(w.drains, d)

	const grace = 200 * time.Millisecond
	start := time.Now()
	truncated := w.waitDrains(grace)
	elapsed := time.Since(start)

	if !truncated {
		t.Fatal("waitDrains reported a clean drain; an fd held outside the group " +
			"means no EOF ever arrives, so it must report that it gave up")
	}
	if elapsed < grace {
		t.Errorf("waitDrains returned after %v, before its %v grace; it must wait "+
			"out the grace before truncating, or output still in flight is lost",
			elapsed, grace)
	}
	// Generous, because this asserts boundedness, not punctuality: the figure
	// that matters is that it returns at all, and a loaded CI host must not turn
	// that into a flake.
	if limit := grace + 5*time.Second; elapsed > limit {
		t.Errorf("waitDrains took %v, want it bounded by roughly the %v grace", elapsed, grace)
	}

	// Giving up must also end the copy goroutine. If it did not, every stalled
	// request would leak one goroutine parked on a pipe nobody will ever close
	// — which is the leak this whole file exists to prevent, just moved.
	select {
	case <-d.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the copy goroutine is still running after waitDrains gave up; " +
			"force-closing the read ends must end every copy")
	}
}
