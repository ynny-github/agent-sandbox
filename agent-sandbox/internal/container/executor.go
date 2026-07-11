package container

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/docker/cli/cli/command"
	"github.com/docker/docker/api/types/build"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	archive "github.com/moby/go-archive"
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

// Up runs the fail-closed consistency check, then ensures the image, sandbox
// network, and container exist, connects the project network (if any), and
// starts the container. It is idempotent: calling Up again on an already
// running sandbox succeeds without error.
func (e *ContainerExecutor) Up(ctx context.Context) error {
	if err := CheckNetworkConsistency(ctx, e.dockerCLI, !e.spec.Internal, e.spec.ExternalNetwork); err != nil {
		return err
	}
	if err := e.ensureImage(ctx); err != nil {
		return err
	}
	if err := e.ensureNetwork(ctx); err != nil {
		return err
	}
	// Reuse an existing (possibly stopped) container if present.
	id, err := e.findContainerID(ctx, false)
	if err != nil {
		return err
	}
	if id == "" {
		id, err = e.createContainer(ctx)
		if err != nil {
			return err
		}
	}
	if e.spec.ExternalNetwork != "" {
		if err := e.dockerCLI.Client().NetworkConnect(ctx, e.spec.ExternalNetwork, id, &dockernetwork.EndpointSettings{}); err != nil && !isAlreadyConnected(err) {
			return fmt.Errorf("executor: connect project network %q: %w", e.spec.ExternalNetwork, err)
		}
	}
	if err := e.dockerCLI.Client().ContainerStart(ctx, id, dockercontainer.StartOptions{}); err != nil {
		return fmt.Errorf("executor: start container: %w", err)
	}
	return nil
}

func isAlreadyConnected(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already exists in network")
}

// ensureImage builds e.spec.ImageTag from the build context if it is absent.
func (e *ContainerExecutor) ensureImage(ctx context.Context) error {
	imgs, err := e.dockerCLI.Client().ImageList(ctx, image.ListOptions{
		Filters: filters.NewArgs(filters.Arg("reference", e.spec.ImageTag)),
	})
	if err != nil {
		return fmt.Errorf("executor: list images: %w", err)
	}
	if len(imgs) > 0 {
		return nil
	}
	tar, err := archive.TarWithOptions(e.spec.BuildContext, &archive.TarOptions{})
	if err != nil {
		return fmt.Errorf("executor: tar build context: %w", err)
	}
	defer tar.Close()
	resp, err := e.dockerCLI.Client().ImageBuild(ctx, tar, build.ImageBuildOptions{
		Dockerfile: e.spec.Dockerfile,
		Tags:       []string{e.spec.ImageTag},
		Remove:     true,
	})
	if err != nil {
		return fmt.Errorf("executor: image build: %w", err)
	}
	defer resp.Body.Close()
	// Drain the build output to stderr; a build failure surfaces in the stream.
	if _, err := io.Copy(os.Stderr, resp.Body); err != nil {
		return fmt.Errorf("executor: read build output: %w", err)
	}
	return nil
}

// ensureNetwork creates the sandbox network if it does not exist.
func (e *ContainerExecutor) ensureNetwork(ctx context.Context) error {
	_, err := e.dockerCLI.Client().NetworkInspect(ctx, e.spec.NetworkName, dockernetwork.InspectOptions{})
	if err == nil {
		return nil
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("executor: inspect sandbox network: %w", err)
	}
	_, err = e.dockerCLI.Client().NetworkCreate(ctx, e.spec.NetworkName, dockernetwork.CreateOptions{
		Driver:   "bridge",
		Internal: e.spec.Internal,
		Labels:   map[string]string{LabelManaged: "true", LabelProjectDir: e.spec.WorkingDir},
	})
	if err != nil {
		return fmt.Errorf("executor: create sandbox network: %w", err)
	}
	return nil
}

func (e *ContainerExecutor) createContainer(ctx context.Context) (string, error) {
	initTrue := true
	created, err := e.dockerCLI.Client().ContainerCreate(ctx,
		&dockercontainer.Config{
			Image:      e.spec.ImageTag,
			User:       fmt.Sprintf("%d:%d", e.spec.UID, e.spec.GID),
			WorkingDir: "/workspace",
			Env:        []string{"HOME=/tmp"},
			Labels:     e.spec.Labels,
		},
		&dockercontainer.HostConfig{
			Binds: []string{e.spec.WorkingDir + ":/workspace"},
			Init:  &initTrue,
		},
		&dockernetwork.NetworkingConfig{
			EndpointsConfig: map[string]*dockernetwork.EndpointSettings{
				e.spec.NetworkName: {},
			},
		},
		nil,
		e.spec.Name,
	)
	if err != nil {
		return "", fmt.Errorf("executor: create container: %w", err)
	}
	return created.ID, nil
}

// Down is a TEMPORARY stub; Task 6 replaces it with real teardown
// (stop/remove container and sandbox network).
func (e *ContainerExecutor) Down(ctx context.Context) error { return nil }
