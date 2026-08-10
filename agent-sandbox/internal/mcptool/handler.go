package mcptool

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/output"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/router"
)

// CommandRunner is the engine's sandbox-execution interface, re-exported so
// existing callers (serve.go, tests) keep their import.
type CommandRunner = router.CommandRunner

// DropRule is the router's drop rule (pattern + optional message), re-exported
// so callers can build a HandlerConfig without importing router directly.
type DropRule = router.DropRule

type HandlerConfig struct {
	OutputDir      string
	AllowPatterns  []string
	DropRules      []DropRule
	CommandRunner  CommandRunner
	EnvPassthrough []string
}

func HandleRunCommand(ctx context.Context, cmd string, cfg HandlerConfig) (*mcp.CallToolResult, any, error) {
	files, err := output.CreateFiles(cfg.OutputDir)
	if err != nil {
		return errorResult(fmt.Sprintf("output: %v", err)), nil, nil
	}

	exitCode, runErr := router.New(router.Config{
		AllowPatterns:  cfg.AllowPatterns,
		DropRules:      cfg.DropRules,
		CommandRunner:  cfg.CommandRunner,
		EnvPassthrough: cfg.EnvPassthrough,
	}).Run(ctx, cmd, files.Stdout, files.Stderr)

	closeErr := files.Close()
	if runErr != nil {
		return errorResult(runErr.Error()), nil, nil
	}
	if closeErr != nil {
		return errorResult(fmt.Sprintf("output close: %v", closeErr)), nil, nil
	}
	return BuildResponse(exitCode, files), nil, nil
}
