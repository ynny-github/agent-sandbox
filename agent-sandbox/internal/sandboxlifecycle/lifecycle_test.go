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
	policyErr  error
	upCalls    int
	policyCalls int
}

func (f *fakeSandbox) IsRunning(context.Context) (bool, error) { return f.running, f.runningErr }
func (f *fakeSandbox) Up(context.Context) error                { f.upCalls++; return f.upErr }
func (f *fakeSandbox) ApplyNetworkPolicy(context.Context) error {
	f.policyCalls++
	return f.policyErr
}
func (f *fakeSandbox) Down(context.Context) error { return nil }

func TestEnsure_AlreadyRunning_SkipsUp(t *testing.T) {
	f := &fakeSandbox{running: true}
	started, err := Ensure(context.Background(), f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if started {
		t.Error("startedByUs = true, want false when already running")
	}
	if f.upCalls != 0 || f.policyCalls != 0 {
		t.Errorf("Up/ApplyNetworkPolicy called on already-running sandbox: up=%d policy=%d", f.upCalls, f.policyCalls)
	}
}

func TestEnsure_NotRunning_StartsAndOwns(t *testing.T) {
	f := &fakeSandbox{running: false}
	started, err := Ensure(context.Background(), f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !started {
		t.Error("startedByUs = false, want true when we started it")
	}
	if f.upCalls != 1 || f.policyCalls != 1 {
		t.Errorf("expected Up and ApplyNetworkPolicy once each; up=%d policy=%d", f.upCalls, f.policyCalls)
	}
}

func TestEnsure_IsRunningError_Fails(t *testing.T) {
	f := &fakeSandbox{runningErr: errors.New("docker down")}
	started, err := Ensure(context.Background(), f)
	if err == nil {
		t.Fatal("expected error when IsRunning fails, got nil")
	}
	if started {
		t.Error("startedByUs must be false when IsRunning fails")
	}
}

func TestEnsure_UpError_Fails(t *testing.T) {
	f := &fakeSandbox{running: false, upErr: errors.New("build failed")}
	started, err := Ensure(context.Background(), f)
	if err == nil {
		t.Fatal("expected error when Up fails, got nil")
	}
	if started {
		t.Error("startedByUs must be false when Up fails")
	}
	if f.policyCalls != 0 {
		t.Error("ApplyNetworkPolicy must not run after Up failed")
	}
}

func TestEnsure_PolicyError_Fails(t *testing.T) {
	f := &fakeSandbox{running: false, policyErr: errors.New("net policy failed")}
	started, err := Ensure(context.Background(), f)
	if err == nil {
		t.Fatal("expected error when ApplyNetworkPolicy fails, got nil")
	}
	if started {
		t.Error("startedByUs must be false when ApplyNetworkPolicy fails")
	}
}
