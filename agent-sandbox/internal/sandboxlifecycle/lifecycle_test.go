package sandboxlifecycle

import (
	"context"
	"errors"
	"testing"
)

// fakeSandbox records calls and returns programmed results.
type fakeSandbox struct {
	running    bool
	runningErr error
	upErr      error
	upCalls    int
}

func (f *fakeSandbox) IsRunning(context.Context) (bool, error) { return f.running, f.runningErr }
func (f *fakeSandbox) Up(context.Context) error                { f.upCalls++; return f.upErr }
func (f *fakeSandbox) Down(context.Context) error              { return nil }

func TestEnsure_AlreadyRunning_SkipsUp(t *testing.T) {
	f := &fakeSandbox{running: true}
	started, err := Ensure(context.Background(), f, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if started {
		t.Error("startedByUs = true, want false when already running")
	}
	if f.upCalls != 0 {
		t.Errorf("Up called on already-running sandbox: up=%d", f.upCalls)
	}
}

func TestEnsure_NotRunning_StartsAndOwns(t *testing.T) {
	f := &fakeSandbox{running: false}
	started, err := Ensure(context.Background(), f, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !started {
		t.Error("startedByUs = false, want true when we started it")
	}
	if f.upCalls != 1 {
		t.Errorf("expected Up once; up=%d", f.upCalls)
	}
}

func TestEnsure_IsRunningError_Fails(t *testing.T) {
	f := &fakeSandbox{runningErr: errors.New("docker down")}
	started, err := Ensure(context.Background(), f, nil)
	if err == nil {
		t.Fatal("expected error when IsRunning fails, got nil")
	}
	if started {
		t.Error("startedByUs must be false when IsRunning fails")
	}
}

func TestEnsure_UpError_Fails(t *testing.T) {
	f := &fakeSandbox{running: false, upErr: errors.New("build failed")}
	started, err := Ensure(context.Background(), f, nil)
	if err == nil {
		t.Fatal("expected error when Up fails, got nil")
	}
	if started {
		t.Error("startedByUs must be false when Up fails")
	}
}

func TestEnsure_ConfirmError_AbortsBeforeUp(t *testing.T) {
	f := &fakeSandbox{running: false}
	sentinel := errors.New("declined")
	started, err := Ensure(context.Background(), f, func() error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	if started {
		t.Error("startedByUs must be false when confirm fails")
	}
	if f.upCalls != 0 {
		t.Errorf("Up must not run when confirm fails; upCalls=%d", f.upCalls)
	}
}

func TestEnsure_ConfirmOK_StartsUp(t *testing.T) {
	f := &fakeSandbox{running: false}
	started, err := Ensure(context.Background(), f, func() error { return nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !started || f.upCalls != 1 {
		t.Errorf("expected Up once and started=true; up=%d started=%v", f.upCalls, started)
	}
}

func TestEnsure_AlreadyRunning_SkipsConfirm(t *testing.T) {
	confirmCalled := false
	f := &fakeSandbox{running: true}
	_, err := Ensure(context.Background(), f, func() error { confirmCalled = true; return nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmCalled {
		t.Error("confirm must not run when the sandbox is already running")
	}
}
