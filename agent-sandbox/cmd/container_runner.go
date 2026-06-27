package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/docker/cli/cli/command"
	cliflags "github.com/docker/cli/cli/flags"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/engine"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/executor"
)

// newComposeContainerRunner builds a Docker-Compose-backed container runner.
// The returned cleanup closes the Docker client and must be called by the
// caller once the runner is no longer needed.
func newComposeContainerRunner(ctx context.Context, cfg *config.Config) (engine.ContainerRunner, func(), error) {
	dockerCli, err := command.NewDockerCli(
		command.WithOutputStream(os.Stderr),
		command.WithErrorStream(os.Stderr),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("docker cli error: %w", err)
	}
	if err := dockerCli.Initialize(cliflags.NewClientOptions()); err != nil {
		return nil, nil, fmt.Errorf("docker cli initialize: %w", err)
	}
	cleanup := func() { dockerCli.Client().Close() }

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := dockerCli.Client().Ping(pingCtx); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("docker daemon error: %w", err)
	}

	detectCtx, detectCancel := context.WithTimeout(ctx, 5*time.Second)
	defer detectCancel()
	externalNetwork := executor.DetectProjectNetwork(detectCtx, dockerCli, cfg.Sandbox.Container.ExternalNetwork)

	project, err := executor.NewSandboxProject(
		os.Getpid(),
		os.Getuid(),
		os.Getgid(),
		cfg.Sandbox.Container.BuildContext,
		cfg.Sandbox.Container.Dockerfile,
		cfg.Sandbox.Container.Image,
		cfg.Sandbox.Network.AllowCIDRs,
		cfg.Sandbox.Network.AllowHosts,
		externalNetwork,
	)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("sandbox project: %w", err)
	}

	return executor.NewComposeExecutor(dockerCli, project), cleanup, nil
}
