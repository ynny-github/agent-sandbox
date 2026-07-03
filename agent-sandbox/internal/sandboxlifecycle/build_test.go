package sandboxlifecycle

import (
	"context"
	"testing"
)

func TestResult_Started_ReflectsField(t *testing.T) {
	if (&Result{StartedByUs: true}).Started() != true {
		t.Error("Started() should return StartedByUs")
	}
	if (&Result{StartedByUs: false}).Started() != false {
		t.Error("Started() should return StartedByUs")
	}
}

func TestResult_Close_NilCleanupIsSafe(t *testing.T) {
	r := &Result{} // cleanup is nil
	r.Close()      // must not panic
}

func TestResult_Close_CallsCleanupOnce(t *testing.T) {
	calls := 0
	r := &Result{cleanup: func() { calls++ }}
	r.Close()
	r.Close()
	if calls != 1 {
		t.Errorf("cleanup called %d times, want 1", calls)
	}
}

// compile-time guard: *Result must satisfy Down's context signature used by the launcher.
var _ = func() { var r *Result; _ = r.Down(context.Background()) }
