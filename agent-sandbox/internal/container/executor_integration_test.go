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
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockernetwork "github.com/docker/docker/api/types/network"
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

// TestUp_ReconcilesEgressDriftOnAllowExternalFlip reproduces the security
// regression where a stopped container + a persistent sandbox network from a
// previous allow_external=true session are silently reused by a later Up
// with allow_external=false, leaving the sandbox on a non-internal network
// despite the fail-closed intent. Up must detect the drift and recreate the
// network (and container) with the current spec's egress posture.
func TestUp_ReconcilesEgressDriftOnAllowExternalFlip(t *testing.T) {
	cli := newITCli(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	spec1, err := container.NewSandboxSpec(1000, 1000, "../../../docker/sandbox", "Dockerfile", "drifttest", true, "")
	if err != nil {
		t.Fatal(err)
	}
	ex1 := container.NewContainerExecutor(cli, spec1)
	if err := ex1.Up(ctx); err != nil {
		t.Fatalf("first Up: %v", err)
	}

	inspect, err := cli.Client().NetworkInspect(ctx, spec1.NetworkName, dockernetwork.InspectOptions{})
	if err != nil {
		t.Fatalf("inspect sandbox network after first Up: %v", err)
	}
	if inspect.Internal {
		t.Fatalf("expected sandbox network Internal=false after allow_external=true Up, got Internal=true")
	}

	// Simulate the "stopped container, persistent network" state left behind
	// by e.g. a host reboot: stop the managed container without removing it
	// or its network.
	containers, err := cli.Client().ContainerList(ctx, dockercontainer.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", container.LabelManaged+"=true"),
			filters.Arg("label", container.LabelProjectDir+"="+spec1.WorkingDir),
		),
	})
	if err != nil {
		t.Fatalf("list managed containers: %v", err)
	}
	if len(containers) == 0 {
		t.Fatal("expected a managed container to stop")
	}
	if err := cli.Client().ContainerStop(ctx, containers[0].ID, dockercontainer.StopOptions{}); err != nil {
		t.Fatalf("stop managed container: %v", err)
	}

	spec2, err := container.NewSandboxSpec(1000, 1000, "../../../docker/sandbox", "Dockerfile", "drifttest", false, "")
	if err != nil {
		t.Fatal(err)
	}
	ex2 := container.NewContainerExecutor(cli, spec2)
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_ = ex2.Down(dctx)
	})

	if err := ex2.Up(ctx); err != nil {
		t.Fatalf("second Up (allow_external=false): %v", err)
	}

	inspect, err = cli.Client().NetworkInspect(ctx, spec2.NetworkName, dockernetwork.InspectOptions{})
	if err != nil {
		t.Fatalf("inspect sandbox network after second Up: %v", err)
	}
	if !inspect.Internal {
		t.Fatal("expected sandbox network Internal=true after reconciling drift to allow_external=false, got Internal=false")
	}

	running, err := ex2.IsRunning(ctx)
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if !running {
		t.Fatal("expected sandbox running after second Up")
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
