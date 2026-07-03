package claude

import (
	"errors"
	"os/exec"
	"testing"
)

func TestExitCode_NilIsZero(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Errorf("exitCode(nil) = %d, want 0", got)
	}
}

func TestExitCode_GenericErrorIsOne(t *testing.T) {
	if got := exitCode(errors.New("start failed")); got != 1 {
		t.Errorf("exitCode(generic) = %d, want 1", got)
	}
}

func TestExitCode_ExitErrorPropagates(t *testing.T) {
	// Produce a real *exec.ExitError with a known code.
	err := exec.Command("/bin/sh", "-c", "exit 7").Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *exec.ExitError, got %T (%v)", err, err)
	}
	if got := exitCode(err); got != 7 {
		t.Errorf("exitCode(exit 7) = %d, want 7", got)
	}
}

func TestSuperviseProcess_PropagatesExitCode(t *testing.T) {
	if got := superviseProcess("/bin/sh", []string{"sh", "-c", "exit 0"}); got != 0 {
		t.Errorf("supervise exit 0 = %d, want 0", got)
	}
	if got := superviseProcess("/bin/sh", []string{"sh", "-c", "exit 7"}); got != 7 {
		t.Errorf("supervise exit 7 = %d, want 7", got)
	}
}

func TestSuperviseProcess_StartFailureIsOne(t *testing.T) {
	if got := superviseProcess("/nonexistent/binary", []string{"nope"}); got != 1 {
		t.Errorf("supervise of missing binary = %d, want 1", got)
	}
}
