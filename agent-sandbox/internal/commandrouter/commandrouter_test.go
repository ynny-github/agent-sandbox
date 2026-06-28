package commandrouter_test

import (
	"context"
	"testing"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/commandrouter"
)

func TestRunBuffered_HostEcho(t *testing.T) {
	s := commandrouter.New(commandrouter.Config{AllowPatterns: []string{"echo *"}})
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

func TestNeedsContainer(t *testing.T) {
	s := commandrouter.New(commandrouter.Config{AllowPatterns: []string{"echo *"}})
	got, err := s.NeedsContainer("echo hi")
	if err != nil {
		t.Fatalf("NeedsContainer error: %v", err)
	}
	if got {
		t.Fatalf("NeedsContainer(host-allowed) = true, want false")
	}
	got, err = s.NeedsContainer("python script.py")
	if err != nil {
		t.Fatalf("NeedsContainer error: %v", err)
	}
	if !got {
		t.Fatalf("NeedsContainer(unmatched) = false, want true")
	}
}
