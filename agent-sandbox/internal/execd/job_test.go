package execd_test

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/execd"
)

func TestJobTerminateReachesGrandchildren(t *testing.T) {
	j := execd.NewJob()
	cmd := exec.Command("sh", "-c", "sleep 2971 & wait")
	if err := j.Start(cmd); err != nil {
		t.Fatal(err)
	}
	// t.Cleanup rather than a trailing kill, so a leaked "sleep 2971" cannot
	// survive this test on any exit path (t.Fatal/t.Errorf included) and
	// poison the next test's pgrep.
	t.Cleanup(func() {
		j.Terminate()
		cmd.Wait()
		exec.Command("pkill", "-f", "sleep 2971").Run()
	})

	// Wait for the grandchild to actually exist before tearing down. A fixed
	// sleep here would be fail-open: if the shell had not forked sleep 2971
	// yet, Terminate would kill it before the grandchild existed, pgrep would
	// find nothing, and the test would pass without exercising the defect it
	// exists to catch. This is the one test guarding that defect, so it polls
	// for the observable fact instead of assuming a duration is long enough.
	deadline := time.Now().Add(2 * time.Second)
	for {
		out, _ := exec.Command("pgrep", "-f", "sleep 2971").Output()
		if len(strings.Fields(string(out))) != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("grandchild never appeared; sleep 2971 was not observed running")
		}
		time.Sleep(10 * time.Millisecond)
	}
	j.Terminate()
	cmd.Wait()

	out, _ := exec.Command("pgrep", "-f", "sleep 2971").Output()
	if pids := strings.Fields(string(out)); len(pids) != 0 {
		t.Errorf("grandchildren survived Terminate: %v", pids)
	}
}

// This test pins a behaviour, not an implementation: "Terminate does not pay
// the grace period when nothing is alive." Job.Terminate satisfies that with
// an explicit early return before the grace loop, but the grace loop's own
// first-iteration check would also satisfy it on its own — more than one
// correct implementation passes this test, and that is intentional.
func TestJobTerminateIsFastWhenNothingIsAlive(t *testing.T) {
	j := execd.NewJob()
	cmd := exec.Command("true")
	if err := j.Start(cmd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Terminate() })
	cmd.Wait()

	start := time.Now()
	j.Terminate()
	if d := time.Since(start); d > execd.TerminateGrace/2 {
		t.Errorf("Terminate took %v with nothing to kill; want it to return at once", d)
	}
}

func TestJobSignalReachesTheCommand(t *testing.T) {
	j := execd.NewJob()
	cmd := exec.Command("sh", "-c", "trap 'exit 42' TERM; sleep 5193 & wait")
	if err := j.Start(cmd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		j.Terminate()
		cmd.Wait()
		// A distinctive duration, as in TestJobTerminateReachesGrandchildren,
		// so this sweep can only ever match its own process and not any
		// "sleep 5" belonging to the user's own session.
		exec.Command("pkill", "-f", "sleep 5193").Run()
	})

	time.Sleep(300 * time.Millisecond)
	j.Signal(syscall.SIGTERM)
	err := cmd.Wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 42 {
		t.Errorf("wait = %v, want exit status 42 from the trap", err)
	}
	j.Terminate()
}
