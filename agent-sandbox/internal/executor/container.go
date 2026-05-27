package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/cli/cli"
	"github.com/docker/cli/cli/command"
	cliflags "github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v2/pkg/api"
	"github.com/docker/compose/v2/pkg/compose"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
)

type CleanResult struct {
	Containers int
	Networks   int
}

type ComposeExecutor struct {
	dockerCLI       command.Cli
	project         *composetypes.Project
	nonoProfile     string
	nonoYoloProfile string
	readyCh         chan struct{}
	readyErr        error
}

func NewComposeExecutor(dockerCLI command.Cli, project *composetypes.Project, nonoProfile, nonoYoloProfile string) *ComposeExecutor {
	return &ComposeExecutor{
		dockerCLI:       dockerCLI,
		project:         project,
		nonoProfile:     nonoProfile,
		nonoYoloProfile: nonoYoloProfile,
	}
}

// StartBackground runs Up and ApplyNetworkPolicy in a goroutine. Call WaitReady
// before issuing commands to ensure the sandbox is available.
func (e *ComposeExecutor) StartBackground(ctx context.Context) {
	e.readyCh = make(chan struct{})
	go func() {
		defer close(e.readyCh)
		if err := e.Up(ctx); err != nil {
			e.readyErr = fmt.Errorf("up: %w", err)
			return
		}
		if err := e.ApplyNetworkPolicy(ctx); err != nil {
			e.readyErr = fmt.Errorf("network policy: %w", err)
		}
	}()
}

// WaitReady blocks until StartBackground completes or ctx is cancelled.
func (e *ComposeExecutor) WaitReady(ctx context.Context) error {
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

func (e *ComposeExecutor) Up(ctx context.Context) error {
	svc := compose.NewComposeService(e.dockerCLI)
	return svc.Up(ctx, e.project, api.UpOptions{
		Create: api.CreateOptions{
			Build: &api.BuildOptions{
				Progress: "quiet",
				Quiet:    true,
				Out:      os.Stderr,
			},
		},
	})
}

func (e *ComposeExecutor) Down(ctx context.Context) error {
	svc := compose.NewComposeService(e.dockerCLI)
	return svc.Down(ctx, e.project.Name, api.DownOptions{})
}

func (e *ComposeExecutor) IsRunning(ctx context.Context) (bool, error) {
	containers, err := e.dockerCLI.Client().ContainerList(ctx, dockercontainer.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", fmt.Sprintf("com.docker.compose.project=%s", e.project.Name)),
			filters.Arg("label", fmt.Sprintf("com.docker.compose.service=%s", SandboxServiceName)),
			filters.Arg("label", "com.docker.compose.oneoff=False"),
			filters.Arg("status", "running"),
		),
	})
	if err != nil {
		return false, fmt.Errorf("executor: list running workspace containers: %w", err)
	}
	return len(containers) > 0, nil
}

func (e *ComposeExecutor) CleanStale(ctx context.Context) (CleanResult, error) {
	var result CleanResult
	containers, err := e.dockerCLI.Client().ContainerList(ctx, dockercontainer.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "cr.managed=true")),
	})
	if err != nil {
		return result, fmt.Errorf("executor: list containers: %w", err)
	}
	for _, c := range containers {
		if removeErr := e.dockerCLI.Client().ContainerRemove(ctx, c.ID, dockercontainer.RemoveOptions{Force: true}); removeErr != nil {
			if !errdefs.IsNotFound(removeErr) {
				fmt.Fprintf(os.Stderr, "executor: remove managed container %s: %v\n", c.ID[:12], removeErr)
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
		if removeErr := e.dockerCLI.Client().NetworkRemove(ctx, n.ID); removeErr != nil {
			if !errdefs.IsNotFound(removeErr) {
				fmt.Fprintf(os.Stderr, "executor: remove network %s: %v\n", n.Name, removeErr)
			}
		} else {
			result.Networks++
		}
	}
	return result, nil
}

// ApplyNetworkPolicy disconnects the workspace container from the default network
// and connects it to sandbox_internal, enforcing proxy-only outbound access.
// Call this after Up() completes.
func (e *ComposeExecutor) ApplyNetworkPolicy(ctx context.Context) error {
	defaultNet := e.project.Name + "_default"
	internalNet := e.project.Name + "_sandbox_internal"

	containers, err := e.dockerCLI.Client().ContainerList(ctx, dockercontainer.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", fmt.Sprintf("com.docker.compose.project=%s", e.project.Name)),
			filters.Arg("label", fmt.Sprintf("com.docker.compose.service=%s", SandboxServiceName)),
		),
	})
	if err != nil {
		return fmt.Errorf("executor: list workspace containers: %w", err)
	}
	if len(containers) == 0 {
		return fmt.Errorf("executor: workspace container not found after Up")
	}
	containerID := containers[0].ID

	if err := e.dockerCLI.Client().NetworkConnect(ctx, internalNet, containerID, nil); err != nil {
		return fmt.Errorf("executor: connect workspace to sandbox_internal: %w", err)
	}
	if err := e.dockerCLI.Client().NetworkDisconnect(ctx, defaultNet, containerID, false); err != nil {
		return fmt.Errorf("executor: disconnect workspace from default: %w", err)
	}
	return nil
}

func (e *ComposeExecutor) RunContainer(ctx context.Context, serviceName, cmd string, env []string, stdout, stderr io.Writer) (int, error) {
	return e.runContainerCommand(ctx, serviceName, buildNonoCommand(ctx, cmd, e.activeNonoProfile()), env, stdout, stderr)
}

func (e *ComposeExecutor) RunContainerDirect(ctx context.Context, serviceName, cmd string, env []string, stdout, stderr io.Writer) (int, error) {
	return e.runContainerCommand(ctx, serviceName, strings.Fields(cmd), env, stdout, stderr)
}

func (e *ComposeExecutor) activeNonoProfile() string {
	if os.Getenv("YOLO_MODE") == "1" {
		return e.nonoYoloProfile
	}
	return e.nonoProfile
}

func (e *ComposeExecutor) runContainerCommand(ctx context.Context, serviceName string, commandTokens []string, env []string, stdout, stderr io.Writer) (int, error) {
	if err := e.WaitReady(ctx); err != nil {
		return 0, fmt.Errorf("executor: sandbox not ready: %w", err)
	}
	perCallCLI, err := command.NewDockerCli(
		command.WithAPIClient(e.dockerCLI.Client()),
		command.WithOutputStream(stdout),
		command.WithErrorStream(stderr),
	)
	if err != nil {
		return 0, fmt.Errorf("executor: create cli: %w", err)
	}
	if err := perCallCLI.Initialize(cliflags.NewClientOptions()); err != nil {
		return 0, fmt.Errorf("executor: initialize cli: %w", err)
	}

	svc := compose.NewComposeService(perCallCLI)
	exitCode, err := svc.Exec(ctx, e.project.Name, api.RunOptions{
		Service:     serviceName,
		Command:     commandTokens,
		Tty:         false,
		Environment: env,
	})
	if err != nil {
		var statusErr cli.StatusError
		if errors.As(err, &statusErr) {
			return statusErr.StatusCode, nil
		}
		return 0, fmt.Errorf("executor: exec: %w", err)
	}
	return exitCode, nil
}

func buildNonoCommand(ctx context.Context, cmd, profile string) []string {
	tokens := strings.Fields(cmd)
	nonoTokens := append([]string{"nono", "-s", "run", "--profile", profile, "--allow-cwd", "--"}, tokens...)

	deadline, ok := ctx.Deadline()
	if !ok {
		return nonoTokens
	}
	secs := int(time.Until(deadline).Seconds()) - 1
	if secs < 1 {
		secs = 1
	}
	return append([]string{"timeout", strconv.Itoa(secs)}, nonoTokens...)
}
