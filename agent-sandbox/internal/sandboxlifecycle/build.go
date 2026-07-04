package sandboxlifecycle

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/docker/cli/cli/command"
	cliflags "github.com/docker/cli/cli/flags"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/container"
)

// NewExecutor connects to Docker and builds a compose executor for cfg's
// sandbox project (named deterministically from the current directory). The
// returned cleanup closes the Docker client and must be called by the caller.
func NewExecutor(ctx context.Context, cfg *config.Config) (*container.ComposeExecutor, string, func(), error) {
	dockerCli, err := command.NewDockerCli(
		command.WithOutputStream(os.Stderr),
		command.WithErrorStream(os.Stderr),
	)
	if err != nil {
		return nil, "", nil, fmt.Errorf("docker cli error: %w", err)
	}
	if err := dockerCli.Initialize(cliflags.NewClientOptions()); err != nil {
		return nil, "", nil, fmt.Errorf("docker cli initialize: %w", err)
	}
	cleanup := func() { dockerCli.Client().Close() }

	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()
	if _, err := dockerCli.Client().Ping(pingCtx); err != nil {
		cleanup()
		return nil, "", nil, fmt.Errorf("docker daemon error: %w", err)
	}

	detectCtx, detectCancel := context.WithTimeout(ctx, 5*time.Second)
	defer detectCancel()
	externalNetwork := container.DetectProjectNetwork(detectCtx, dockerCli, cfg.Sandbox.Container.ExternalNetwork)

	project, err := container.NewSandboxProject(
		os.Getpid(),
		os.Getuid(),
		os.Getgid(),
		cfg.Sandbox.Container.BuildContext,
		cfg.Sandbox.Container.Dockerfile,
		cfg.Sandbox.Container.Image,
		nil, // TODO(Task 2): signature changes — remove both nils and pass cfg.Sandbox.Network.AllowExternal in the new bool parameter
		nil,
		externalNetwork,
	)
	if err != nil {
		cleanup()
		return nil, "", nil, fmt.Errorf("sandbox project: %w", err)
	}

	return container.NewComposeExecutor(dockerCli, project), project.Name, cleanup, nil
}

// Result carries the outcome of EnsureUp: the executor to run/tear down the
// sandbox, its project name, and whether this call started it.
type Result struct {
	Executor    *container.ComposeExecutor
	ProjectName string
	StartedByUs bool

	cleanup func()
	closed  bool
}

// Started reports whether this call started the sandbox (and therefore owns it).
func (r *Result) Started() bool { return r.StartedByUs }

// Down stops and removes the sandbox.
func (r *Result) Down(ctx context.Context) error { return r.Executor.Down(ctx) }

// Close releases the Docker client. Safe to call multiple times.
func (r *Result) Close() {
	if r.closed || r.cleanup == nil {
		return
	}
	r.closed = true
	r.cleanup()
}

// EnsureUp builds the executor for cfg and ensures the sandbox is running.
// On any failure it closes the Docker client and returns the error; Claude
// must not be launched in that case.
func EnsureUp(ctx context.Context, cfg *config.Config) (*Result, error) {
	executor, projectName, cleanup, err := NewExecutor(ctx, cfg)
	if err != nil {
		return nil, err
	}
	startedByUs, err := Ensure(ctx, executor)
	if err != nil {
		cleanup()
		return nil, err
	}
	return &Result{
		Executor:    executor,
		ProjectName: projectName,
		StartedByUs: startedByUs,
		cleanup:     cleanup,
	}, nil
}
