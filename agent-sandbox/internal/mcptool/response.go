package mcptool

import (
	"encoding/json"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/output"
)

type ToolResult struct {
	ExitCode   int    `json:"exit_code"`
	StdoutPath string `json:"stdout_path,omitempty"`
	StderrPath string `json:"stderr_path,omitempty"`
}

func BuildResponse(exitCode int, files *output.Files) *mcp.CallToolResult {
	result := ToolResult{ExitCode: exitCode}
	if hasContent(files.StdoutPath) {
		result.StdoutPath = files.StdoutPath
	}
	if hasContent(files.StderrPath) {
		result.StderrPath = files.StderrPath
	}
	data, _ := json.Marshal(result)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}
}

func hasContent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}
