//go:build integration

package container_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/docker/cli/cli/command"
	cliflags "github.com/docker/cli/cli/flags"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/container"
)

func newITCli(t *testing.T) command.Cli {
	t.Helper()
	cli, err := command.NewDockerCli()
	if err != nil {
		t.Skipf("docker cli unavailable: %v", err)
	}
	if err := cli.Initialize(cliflags.NewClientOptions()); err != nil {
		t.Skipf("docker cli initialize: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Client().Ping(ctx); err != nil {
		t.Skipf("docker daemon unavailable: %v", err)
	}
	return cli
}

func TestIsRunning_FalseWhenAbsent(t *testing.T) {
	cli := newITCli(t)
	spec, err := container.NewSandboxSpec(1000, 1000, "../../../docker/sandbox", "Dockerfile", "notrunning-xyz", false, "")
	if err != nil {
		t.Fatal(err)
	}
	ex := container.NewContainerExecutor(cli, spec)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	running, err := ex.IsRunning(ctx)
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if running {
		t.Fatal("expected not running for a sandbox that was never started")
	}
}

func TestUp_StartsAndIsRunning(t *testing.T) {
	cli := newITCli(t)
	spec, err := container.NewSandboxSpec(1000, 1000, "../../../docker/sandbox", "Dockerfile", "uptest", false, "")
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
	// Idempotent second Up must not error.
	if err := ex.Up(ctx); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	running, err := ex.IsRunning(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !running {
		t.Fatal("expected sandbox running after Up")
	}
}

func TestRunContainer_ExitCodeAndOutput(t *testing.T) {
	cli := newITCli(t)
	spec, err := container.NewSandboxSpec(1000, 1000, "../../../docker/sandbox", "Dockerfile", "exectest", false, "")
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

	var out bytes.Buffer
	code, err := ex.RunContainer(ctx, []string{"echo", "hello"}, nil, nil, &out, &out)
	if err != nil {
		t.Fatalf("RunContainer: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "hello") {
		t.Errorf("output = %q, want to contain hello", out.String())
	}

	code, err = ex.RunContainer(ctx, []string{"sh", "-c", "exit 3"}, nil, nil, &out, &out)
	if err != nil {
		t.Fatalf("RunContainer(exit 3): %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

func TestDown_RemovesContainerAndNetwork(t *testing.T) {
	cli := newITCli(t)
	spec, err := container.NewSandboxSpec(1000, 1000, "../../../docker/sandbox", "Dockerfile", "downtest", false, "")
	if err != nil {
		t.Fatal(err)
	}
	ex := container.NewContainerExecutor(cli, spec)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := ex.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := ex.Down(ctx); err != nil {
		t.Fatalf("Down: %v", err)
	}
	running, err := ex.IsRunning(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if running {
		t.Fatal("expected not running after Down")
	}
}
