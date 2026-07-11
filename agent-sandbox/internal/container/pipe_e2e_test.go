//go:build integration

package container_test

import (
	"context"
	"testing"
	"time"

	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/container"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
)

func TestPipe_E2E(t *testing.T) {
	cli := newITCli(t)
	spec, err := container.NewSandboxSpec(1000, 1000, "../../../docker/sandbox", "Dockerfile", "pipee2etest", false, "")
	if err != nil {
		t.Fatal(err)
	}
	ex := container.NewContainerExecutor(cli, spec)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_ = ex.Down(dctx)
	})
	if err := ex.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}

	s := router.New(router.Config{
		AllowPatterns:   []string{"echo *"}, // echo on host, tr in container
		ContainerRunner: ex,
	})
	res, err := s.RunBuffered(ctx, "echo hello | tr a-z A-Z")
	if err != nil {
		t.Fatalf("RunBuffered error: %v", err)
	}
	if res.ExitCode != 0 || string(res.Stdout) != "HELLO\n" {
		t.Fatalf("code=%d stdout=%q, want 0 / %q", res.ExitCode, res.Stdout, "HELLO\n")
	}
}
