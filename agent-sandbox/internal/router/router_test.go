package router_test

import (
	"context"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
)

func TestRunBuffered_HostEcho(t *testing.T) {
	s := router.New(router.Config{AllowPatterns: []string{"echo *"}})
	res, err := s.RunBuffered(context.Background(), "echo hello")
	if err != nil {
		t.Fatalf("RunBuffered error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if string(res.Stdout) != "hello\n" {
		t.Fatalf("Stdout = %q, want %q", res.Stdout, "hello\n")
	}
}

func TestNeedsSandbox(t *testing.T) {
	s := router.New(router.Config{AllowPatterns: []string{"echo *"}})
	got, err := s.NeedsSandbox("echo hi")
	if err != nil {
		t.Fatalf("NeedsSandbox error: %v", err)
	}
	if got {
		t.Fatalf("NeedsSandbox(host-allowed) = true, want false")
	}
	got, err = s.NeedsSandbox("python script.py")
	if err != nil {
		t.Fatalf("NeedsSandbox error: %v", err)
	}
	if !got {
		t.Fatalf("NeedsSandbox(unmatched) = false, want true")
	}
}
