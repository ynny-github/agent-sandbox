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
	"github.com/docker/docker/pkg/stdcopy"
	archive "github.com/moby/go-archive"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
)

// CleanResult reports how many containers and networks a prune removed.
type CleanResult struct {
	Containers int
	Networks   int
}

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

// RunSandboxed execs argv inside the running sandbox container, streaming
// stdout/stderr and optionally forwarding stdin, and returns the process exit
// code. It returns router.ErrSandboxNotRunning if the sandbox container is
// not currently running.
func (e *ContainerExecutor) RunSandboxed(ctx context.Context, argv []string, env []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if err := e.WaitReady(ctx); err != nil {
		return 0, fmt.Errorf("executor: sandbox not ready: %w", err)
	}
	id, err := e.findContainerID(ctx, true)
	if err != nil {
		return 0, fmt.Errorf("executor: check sandbox status: %w", err)
	}
	if id == "" {
		return 0, router.ErrSandboxNotRunning
	}
	execResp, err := e.dockerCLI.Client().ContainerExecCreate(ctx, id, dockercontainer.ExecOptions{
		Cmd:          argv,
		Env:          env,
		AttachStdout: true,
		AttachStderr: true,
		AttachStdin:  stdin != nil,
		Tty:          false,
	})
	if err != nil {
		return 0, fmt.Errorf("executor: exec create: %w", err)
	}
	att, err := e.dockerCLI.Client().ContainerExecAttach(ctx, execResp.ID, dockercontainer.ExecAttachOptions{})
	if err != nil {
		return 0, fmt.Errorf("executor: exec attach: %w", err)
	}
	defer att.Close()

	if stdin != nil {
		go func() {
			_, _ = io.Copy(att.Conn, stdin)
			_ = att.CloseWrite()
		}()
	}
	// Non-TTY streams are multiplexed; demux into stdout/stderr.
	if _, err := stdcopy.StdCopy(stdout, stderr, att.Reader); err != nil {
		return 0, fmt.Errorf("executor: copy exec output: %w", err)
	}
	inspect, err := e.dockerCLI.Client().ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return 0, fmt.Errorf("executor: exec inspect: %w", err)
	}
	return inspect.ExitCode, nil
}

// Up runs the fail-closed consistency check, then ensures the image, sandbox
// network, and container exist, connects the project network (if any), and
// starts the container. It is idempotent: calling Up again on an already
// running sandbox succeeds without error.
func (e *ContainerExecutor) Up(ctx context.Context) error {
	if err := CheckNetworkConsistency(ctx, e.dockerCLI, !e.spec.Internal, e.spec.ExternalNetwork); err != nil {
		return err
	}
	if err := e.reconcileDrift(ctx); err != nil {
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

// reconcileDrift tears down the sandbox network (and any container attached to
// it) when its egress posture no longer matches the spec, so Up rebuilds them
// with the current allow_external setting. Without this, a reused network/
// container from a previous allow_external value would silently persist —
// defeating egress isolation. Mirrors Compose's recreate-on-config-change.
func (e *ContainerExecutor) reconcileDrift(ctx context.Context) error {
	inspect, err := e.dockerCLI.Client().NetworkInspect(ctx, e.spec.NetworkName, dockernetwork.InspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("executor: inspect sandbox network: %w", err)
	}
	if inspect.Internal == e.spec.Internal {
		return nil
	}
	// Egress posture changed — remove container then network so Up recreates them.
	id, err := e.findContainerID(ctx, false)
	if err != nil {
		return err
	}
	if id != "" {
		if err := e.dockerCLI.Client().ContainerRemove(ctx, id, dockercontainer.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("executor: remove drifted container: %w", err)
		}
	}
	if err := e.dockerCLI.Client().NetworkRemove(ctx, inspect.ID); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("executor: remove drifted network: %w", err)
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
			WorkingDir: e.spec.WorkingDir,
			Env:        []string{"HOME=/tmp"},
			Labels:     e.spec.Labels,
		},
		&dockercontainer.HostConfig{
			// Identity mount: the project lives at the same absolute path inside
			// the container as on the host, so paths the agent sees from
			// container-routed shell commands are valid for host-side file tools.
			Binds: []string{e.spec.WorkingDir + ":" + e.spec.WorkingDir},
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

// Down stops and removes the sandbox container and its network.
// The external project network is never touched.
func (e *ContainerExecutor) Down(ctx context.Context) error {
	id, err := e.findContainerID(ctx, false)
	if err != nil {
		return err
	}
	if id != "" {
		if err := e.dockerCLI.Client().ContainerRemove(ctx, id, dockercontainer.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("executor: remove container: %w", err)
		}
	}
	if err := e.dockerCLI.Client().NetworkRemove(ctx, e.spec.NetworkName); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("executor: remove sandbox network: %w", err)
	}
	return nil
}

// DownProject stops and removes a sandbox by its project working directory,
// without needing a full spec.
func DownProject(ctx context.Context, dockerCLI command.Cli, workingDir string) error {
	args := filters.NewArgs(
		filters.Arg("label", LabelManaged+"=true"),
		filters.Arg("label", LabelProjectDir+"="+workingDir),
	)
	containers, err := dockerCLI.Client().ContainerList(ctx, dockercontainer.ListOptions{All: true, Filters: args})
	if err != nil {
		return fmt.Errorf("executor: list containers: %w", err)
	}
	for _, c := range containers {
		if err := dockerCLI.Client().ContainerRemove(ctx, c.ID, dockercontainer.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("executor: remove container %s: %w", c.ID[:12], err)
		}
	}
	networks, err := dockerCLI.Client().NetworkList(ctx, dockernetwork.ListOptions{Filters: args})
	if err != nil {
		return fmt.Errorf("executor: list networks: %w", err)
	}
	for _, n := range networks {
		if err := dockerCLI.Client().NetworkRemove(ctx, n.ID); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("executor: remove network %s: %w", n.Name, err)
		}
	}
	return nil
}

// CleanStale removes all agent-sandbox managed containers and cr-sandbox- networks.
func (e *ContainerExecutor) CleanStale(ctx context.Context) (CleanResult, error) {
	var result CleanResult
	containers, err := e.dockerCLI.Client().ContainerList(ctx, dockercontainer.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", LabelManaged+"=true")),
	})
	if err != nil {
		return result, fmt.Errorf("executor: list containers: %w", err)
	}
	for _, c := range containers {
		if err := e.dockerCLI.Client().ContainerRemove(ctx, c.ID, dockercontainer.RemoveOptions{Force: true}); err != nil {
			if !errdefs.IsNotFound(err) {
				fmt.Fprintf(os.Stderr, "executor: remove managed container %s: %v\n", c.ID[:12], err)
			}
		} else {
			result.Containers++
		}
	}
	networks, err := e.dockerCLI.Client().NetworkList(ctx, dockernetwork.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", "cr-sandbox-")),
	})
	if err != nil {
		return result, fmt.Errorf("executor: list networks: %w", err)
	}
	for _, n := range networks {
		if !strings.HasPrefix(n.Name, "cr-sandbox-") {
			continue
		}
		if err := e.dockerCLI.Client().NetworkRemove(ctx, n.ID); err != nil {
			if !errdefs.IsNotFound(err) {
				fmt.Fprintf(os.Stderr, "executor: remove network %s: %v\n", n.Name, err)
			}
		} else {
			result.Networks++
		}
	}
	return result, nil
}
