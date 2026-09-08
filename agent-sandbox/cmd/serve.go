// agent-sandbox/cmd/serve.go
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/config"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/mcptool"
)

var serveCmd = &cobra.Command{
	Use:   "command-router",
	Short: "Start the MCP server that brokers commands to the sandbox",
	RunE:  runServe,
}

const e2eLightweightEnv = "AGENT_SANDBOX_E2E_LIGHTWEIGHT"

type serveDependencies struct {
	commandRunner mcptool.CommandRunner
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
		OutputDir:     cfg.MCP.CommandOutputDir,
		CommandRunner: deps.commandRunner,
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

	var runner mcptool.CommandRunner
	client, err := broker.NewClientFromEnv()
	if err != nil {
		// Every command now runs through the broker, so a missing command
		// sandbox means every RunCommand call will fail — but refusing to
		// start here would hide the reason behind a dead stdio server, with
		// no actionable message reaching the agent at all. Print the
		// actionable hint instead and let each command fail individually
		// with it (see mcptool.HandleRunCommand).
		if !errors.Is(err, broker.ErrBrokerUnavailable) {
			return err
		}
		fmt.Fprintln(os.Stderr, broker.SandboxNotRunningHint)
		runner = unavailableRunner{err: err}
	} else {
		runner = client
	}

	server := newCommandRouterServer(cfg, serveDependencies{
		commandRunner: runner,
	})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}
