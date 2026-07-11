package container

import (
	"context"
	"fmt"

	"github.com/docker/cli/cli/command"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// NOTE: CleanResult is already declared in container.go and reused here.
// Do not redeclare it (duplicate package-level type = compile error). It is
// relocated into this file in Task 9 when container.go is deleted.

// ContainerExecutor manages one sandbox container through the Docker SDK.
type ContainerExecutor struct {
	dockerCLI command.Cli
	spec      *SandboxSpec
	readyCh   chan struct{}
	readyErr  error
}

func NewContainerExecutor(dockerCLI command.Cli, spec *SandboxSpec) *ContainerExecutor {
	return &ContainerExecutor{dockerCLI: dockerCLI, spec: spec}
}

// StartBackground runs Up in a goroutine; call WaitReady before issuing commands.
func (e *ContainerExecutor) StartBackground(ctx context.Context) {
	e.readyCh = make(chan struct{})
	go func() {
		defer close(e.readyCh)
		if err := e.Up(ctx); err != nil {
			e.readyErr = fmt.Errorf("up: %w", err)
		}
	}()
}

// WaitReady blocks until StartBackground completes or ctx is cancelled.
func (e *ContainerExecutor) WaitReady(ctx context.Context) error {
	if e.readyCh == nil {
		return nil
	}
	select {
	case <-e.readyCh:
		return e.readyErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *ContainerExecutor) managedFilters(running bool) filters.Args {
	args := filters.NewArgs(
		filters.Arg("label", LabelManaged+"=true"),
		filters.Arg("label", LabelProjectDir+"="+e.spec.WorkingDir),
	)
	if running {
		args.Add("status", "running")
	}
	return args
}

// IsRunning reports whether this project's sandbox container is running.
func (e *ContainerExecutor) IsRunning(ctx context.Context) (bool, error) {
	containers, err := e.dockerCLI.Client().ContainerList(ctx, dockercontainer.ListOptions{
		Filters: e.managedFilters(true),
	})
	if err != nil {
		return false, fmt.Errorf("executor: list running sandbox containers: %w", err)
	}
	return len(containers) > 0, nil
}

// findContainerID returns the ID of this project's sandbox container, or "" if none.
func (e *ContainerExecutor) findContainerID(ctx context.Context, running bool) (string, error) {
	containers, err := e.dockerCLI.Client().ContainerList(ctx, dockercontainer.ListOptions{
		All:     !running,
		Filters: e.managedFilters(running),
	})
	if err != nil {
		return "", fmt.Errorf("executor: find sandbox container: %w", err)
	}
	if len(containers) == 0 {
		return "", nil
	}
	return containers[0].ID, nil
}

// Temporary stub; replaced in Task 4.
func (e *ContainerExecutor) Up(ctx context.Context) error { return fmt.Errorf("Up not implemented") }
