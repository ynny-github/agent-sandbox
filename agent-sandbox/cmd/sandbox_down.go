package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/docker/cli/cli/command"
	cliflags "github.com/docker/cli/cli/flags"
	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/container"
)

var sandboxDownCmd = &cobra.Command{
	Use:   "down",
	Short: "Stop and remove the current project sandbox",
	RunE:  runSandboxDown,
}

func init() {
	sandboxCmd.AddCommand(sandboxDownCmd)
}

func runSandboxDown(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	dockerCli, err := command.NewDockerCli()
	if err != nil {
		return fmt.Errorf("docker cli error: %w", err)
	}
	if err := dockerCli.Initialize(cliflags.NewClientOptions()); err != nil {
		return fmt.Errorf("docker cli initialize: %w", err)
	}
	defer dockerCli.Client().Close()

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if _, err := dockerCli.Client().Ping(pingCtx); err != nil {
		return fmt.Errorf("docker daemon error: %w", err)
	}

	detectCtx, detectCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer detectCancel()
	externalNetwork := container.DetectProjectNetwork(detectCtx, dockerCli, cfg.Sandbox.Container.ExternalNetwork)

	project, err := container.NewSandboxProject(
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
		return fmt.Errorf("sandbox project: %w", err)
	}

	composeExecutor := container.NewComposeExecutor(dockerCli, project)
	downCtx, downCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer downCancel()
	if err := composeExecutor.Down(downCtx); err != nil {
		return fmt.Errorf("sandbox down: %w", err)
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "sandbox %s stopped\n", project.Name)
	return nil
}
