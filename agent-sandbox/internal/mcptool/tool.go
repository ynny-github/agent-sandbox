package mcptool

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxTimeoutSeconds = int64(1<<63-1) / int64(time.Second)

const runCommandDescription = `Execute a shell command.

Routing:
- Commands matching allow-patterns run on the host.
- Commands matching drop-patterns are refused.
- All other commands run in the container.

Operator handling:
- Pipe (|): each segment is routed independently; host and container segments
  may be mixed within the same pipeline.
- Sequential operators (&&, ||, ;): each pipeline is routed and executed in order.
- Redirect segments (>, <, >>, 2>) and operators ($(), ` + "`" + `, lone &): run via
  bash -c on whichever side they are routed to; $(), backtick, and lone &
  always fall back to the container.`

type RunCommandInput struct {
	Command        string `json:"command"`
	TimeoutSeconds *int   `json:"timeout_seconds,omitempty"`
}

func Register(s *mcp.Server, cfg HandlerConfig) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "run_command",
		Description: runCommandDescription,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args RunCommandInput) (*mcp.CallToolResult, any, error) {
		if args.TimeoutSeconds != nil {
			if *args.TimeoutSeconds <= 0 {
				return errorResult(fmt.Sprintf("invalid timeout_seconds: %d (must be > 0)", *args.TimeoutSeconds)), nil, nil
			}
			if int64(*args.TimeoutSeconds) > maxTimeoutSeconds {
				return errorResult(fmt.Sprintf("timeout_seconds exceeds maximum supported value (%d)", maxTimeoutSeconds)), nil, nil
			}
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(*args.TimeoutSeconds)*time.Second)
			defer cancel()
		}
		return HandleRunCommand(ctx, args.Command, cfg)
	})
}
