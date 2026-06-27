// agent-sandbox/cmd/serve.go
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
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

	runner, cleanup, err := newComposeContainerRunner(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	server := newCommandRouterServer(cfg, serveDependencies{
		containerRunner: runner,
	})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}
