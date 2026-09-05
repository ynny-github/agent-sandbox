package mcptool

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/output"
)

// CommandRunner is the broker's execution interface, re-exported so existing
// callers keep their import.
type CommandRunner = broker.CommandRunner

type HandlerConfig struct {
	OutputDir     string
	CommandRunner CommandRunner
}

func HandleRunCommand(ctx context.Context, cmd string, cfg HandlerConfig) (*mcp.CallToolResult, any, error) {
	// A nil CommandRunner means the server was started with no broker
	// connection at all (cmd/serve.go's lightweight/E2E path builds
	// HandlerConfig{} directly, without even unavailableRunner). Guard here
	// rather than let the call below panic the whole MCP server process.
	if cfg.CommandRunner == nil {
		return errorResult(broker.SandboxNotRunningHint), nil, nil
	}

	files, err := output.CreateFiles(cfg.OutputDir)
	if err != nil {
		return errorResult(fmt.Sprintf("output: %v", err)), nil, nil
	}

	exitCode, runErr := cfg.CommandRunner.RunCommand(ctx, cmd, nil, files.Stdout, files.Stderr)

	closeErr := files.Close()
	if runErr != nil {
		return errorResult(runErr.Error()), nil, nil
	}
	if closeErr != nil {
		return errorResult(fmt.Sprintf("output close: %v", closeErr)), nil, nil
	}
	return BuildResponse(exitCode, files), nil, nil
}
