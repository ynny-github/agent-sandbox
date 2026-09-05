package mcptool_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/broker"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/mcptool"
)

// mockRunner implements mcptool.CommandRunner.
type mockRunner struct {
	exitCode int
	stdout   string
	stderr   string
	err      error

	// blockUntilCancel, when set, makes RunCommand ignore the canned fields
	// above and instead wait for ctx to be cancelled, returning 124 (the
	// conventional "timed out" status) — used to prove a timeout set on the
	// tool call actually reaches the command runner.
	blockUntilCancel bool
}

func (m *mockRunner) RunCommand(ctx context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if m.blockUntilCancel {
		<-ctx.Done()
		return 124, nil
	}
	if m.stdout != "" {
		io.WriteString(stdout, m.stdout)
	}
	if m.stderr != "" {
		io.WriteString(stderr, m.stderr)
	}
	return m.exitCode, m.err
}

func setupServerWithConfig(t *testing.T, cfg mcptool.HandlerConfig) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	mcptool.Register(server, cfg)

	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func parseToolResult(t *testing.T, res *mcp.CallToolResult) mcptool.ToolResult {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("response has no content")
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] is %T, want *mcp.TextContent", res.Content[0])
	}
	var result mcptool.ToolResult
	if err := json.Unmarshal([]byte(text.Text), &result); err != nil {
		t.Fatalf("unmarshal response JSON: %v (text=%q)", err, text.Text)
	}
	return result
}

func TestRunCommand_ReturnsExitCodeAndStdoutPath(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:     dir,
		CommandRunner: &mockRunner{exitCode: 0, stdout: "test output\n"},
	}
	session := setupServerWithConfig(t, cfg)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "npm test"},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}

	result := parseToolResult(t, res)
	if result.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", result.ExitCode)
	}
	if result.StdoutPath == "" {
		t.Error("stdout_path should be non-empty (runner wrote output)")
	}
	if result.StderrPath != "" {
		t.Errorf("stderr_path should be empty, got %q", result.StderrPath)
	}
}

func TestRunCommand_ReturnsStderrPath(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:     dir,
		CommandRunner: &mockRunner{exitCode: 1, stderr: "boom\n"},
	}
	session := setupServerWithConfig(t, cfg)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "false"},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}

	result := parseToolResult(t, res)
	if result.ExitCode == 0 {
		t.Errorf("exit_code = 0, want non-zero")
	}
	if result.StdoutPath != "" {
		t.Errorf("stdout_path should be empty, got %q", result.StdoutPath)
	}
	if result.StderrPath == "" {
		t.Error("stderr_path should be non-empty (command produced stderr)")
	}
}

func TestRunCommand_NoOutput_OmitsBothPaths(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:     dir,
		CommandRunner: &mockRunner{exitCode: 0},
	}
	session := setupServerWithConfig(t, cfg)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "true"},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}

	result := parseToolResult(t, res)
	if result.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", result.ExitCode)
	}
	if result.StdoutPath != "" {
		t.Errorf("stdout_path should be empty for no-output command, got %q", result.StdoutPath)
	}
	if result.StderrPath != "" {
		t.Errorf("stderr_path should be empty, got %q", result.StderrPath)
	}
}

func TestRunCommand_CommandRunnerError_ReturnsStructuredResponse(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir: dir,
		CommandRunner: &mockRunner{
			exitCode: 0,
			stdout:   "partial output\n",
			err:      errors.New("attach interrupted"),
		},
	}
	session := setupServerWithConfig(t, cfg)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "npm test"},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error for runner error, got: %v", res.Content)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "attach interrupted") {
		t.Errorf("error content = %q, want it to contain runner error", text)
	}
}

// A HandlerConfig with no CommandRunner at all (cmd/serve.go's
// lightweight/E2E path builds one this way) must not panic the server
// process; it must return the same actionable hint printed everywhere else
// the broker is unreachable.
func TestRunCommand_NilCommandRunner_ReturnsStructuredErrorNotPanic(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{OutputDir: dir}
	session := setupServerWithConfig(t, cfg)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "echo hi"},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error for a nil CommandRunner, got: %v", res.Content)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, broker.SandboxNotRunningHint) {
		t.Errorf("error content = %q, want the broker unavailable hint", text)
	}
}

// Ensure mockRunner satisfies the mcptool interface.
var _ mcptool.CommandRunner = (*mockRunner)(nil)

func TestRunCommand_TimeoutSeconds_Zero_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{OutputDir: dir}
	session := setupServerWithConfig(t, cfg)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "echo hi", "timeout_seconds": 0},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for timeout_seconds=0")
	}
}

func TestRunCommand_TimeoutSeconds_Negative_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{OutputDir: dir}
	session := setupServerWithConfig(t, cfg)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "echo hi", "timeout_seconds": -5},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for timeout_seconds=-5")
	}
}

// A timeout set on the tool call must reach the command runner as a
// cancelled context, so a runner that respects ctx can end the command
// promptly instead of running to whatever its own limits are.
func TestRunCommand_TimeoutExpires_ReachesTheRunner(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:     dir,
		CommandRunner: &mockRunner{blockUntilCancel: true},
	}
	session := setupServerWithConfig(t, cfg)

	start := time.Now()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "sleep 60", "timeout_seconds": 1},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("expected timeout in ~1s, took %v", elapsed)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}

	result := parseToolResult(t, res)
	if result.ExitCode != 124 {
		t.Errorf("exit_code = %d, want 124", result.ExitCode)
	}
}

// Ensure math import is used for overflow test in tool_test.go.
var _ = math.MaxInt

func TestRunCommand_WithTimeout_FastCommand_DoesNotInterfere(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:     dir,
		CommandRunner: &mockRunner{exitCode: 0, stdout: "hello\n"},
	}
	session := setupServerWithConfig(t, cfg)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "echo hello", "timeout_seconds": 30},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}

	result := parseToolResult(t, res)
	if result.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0 (timeout should not interfere)", result.ExitCode)
	}
	if result.StdoutPath == "" {
		t.Error("stdout_path should be non-empty")
	}
}

func TestRunCommand_WithoutTimeout_RunsNaturally(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:     dir,
		CommandRunner: &mockRunner{exitCode: 0, stdout: "hello\n"},
	}
	session := setupServerWithConfig(t, cfg)

	// No timeout_seconds in arguments
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "echo hello"},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}

	result := parseToolResult(t, res)
	if result.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", result.ExitCode)
	}
}

func TestRunCommand_WithHugeTimeout_ReturnsToolError(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:     dir,
		CommandRunner: &mockRunner{exitCode: 0, stdout: "hello\n"},
	}
	session := setupServerWithConfig(t, cfg)
	overflowingTimeout := int(math.MaxInt64/int64(time.Second) + 1)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "echo hello", "timeout_seconds": overflowingTimeout},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error for overflowing timeout, got: %v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatal("expected error content")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "timeout_seconds exceeds maximum supported value") {
		t.Fatalf("error content = %q, want timeout overflow message", text)
	}
}

func TestRunCommand_ResponseContainsNoRawOutput(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:     dir,
		CommandRunner: &mockRunner{exitCode: 0, stdout: "supersecretoutput\n"},
	}
	session := setupServerWithConfig(t, cfg)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "echo supersecretoutput"},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}

	// Verify raw output is not in the response body
	if len(res.Content) == 0 {
		t.Fatal("response has no content")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if strings.Contains(text, "supersecretoutput") {
		t.Errorf("response body must not contain raw command output, got: %q", text)
	}
}
