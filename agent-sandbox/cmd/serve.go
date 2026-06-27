// agent-sandbox/cmd/serve.go
package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/docker/cli/cli/command"
	cliflags "github.com/docker/cli/cli/flags"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/executor"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/mcptool"
)

var serveCmd = &cobra.Command{
	Use:   "command-router",
	Short: "Start the MCP command router server",
	RunE:  runServe,
}

const e2eLightweightEnv = "AGENT_SANDBOX_E2E_LIGHTWEIGHT"

type serveDependencies struct {
	containerRunner mcptool.ContainerRunner
}

func init() {
	rootCmd.AddCommand(serveCmd)
}

func newCommandRouterServer(cfg *config.Config, deps serveDependencies) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "command-router",
		Version: buildVersion(),
	}, nil)

	mcptool.Register(server, mcptool.HandlerConfig{
		OutputDir:               cfg.MCP.CommandOutputDir,
		AllowPatterns:           cfg.Sandbox.Command.Allow,
		DropPatterns:            cfg.Sandbox.Command.Drop,
		ContainerRunner:         deps.containerRunner,
		ContainerEnvPassthrough: cfg.Sandbox.Container.EnvPassthrough,
	})

	return server
}

func runLightweightServe(cfg *config.Config) error {
	server := newCommandRouterServer(cfg, serveDependencies{})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

func runServe(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	if os.Getenv(e2eLightweightEnv) == "1" {
		return runLightweightServe(cfg)
	}

	dockerCli, err := command.NewDockerCli(
		command.WithOutputStream(os.Stderr),
		command.WithErrorStream(os.Stderr),
	)
	if err != nil {
		return fmt.Errorf("docker cli error: %w", err)
	}
	if err := dockerCli.Initialize(cliflags.NewClientOptions()); err != nil {
		return fmt.Errorf("docker cli initialize: %w", err)
	}
	defer dockerCli.Client().Close()

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := dockerCli.Client().Ping(pingCtx); err != nil {
		return fmt.Errorf("docker daemon error: %w", err)
	}

	detectCtx, detectCancel := context.WithTimeout(context.Background(), 5*time.Second)
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
		return fmt.Errorf("sandbox project: %w", err)
	}

	composeExecutor := executor.NewComposeExecutor(dockerCli, project)

	server := newCommandRouterServer(cfg, serveDependencies{
		containerRunner: composeExecutor,
	})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}
