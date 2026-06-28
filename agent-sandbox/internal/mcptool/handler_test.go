package mcptool_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/mcptool"
)

// mockRunner implements mcptool.ContainerRunner.
type mockRunner struct {
	exitCode    int
	stdout      string
	stderr      string
	err         error
	capturedEnv []string
}

func (m *mockRunner) RunContainer(ctx context.Context, argv []string, env []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	m.capturedEnv = env
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

func TestRunCommand_HostExecution_ReturnsExitCodeAndStdoutPath(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:     dir,
		AllowPatterns: []string{"echo *"},
	}
	session := setupServerWithConfig(t, cfg)

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
	if result.StdoutPath == "" {
		t.Error("stdout_path should be non-empty (command produced output)")
	}
	if result.StderrPath != "" {
		t.Errorf("stderr_path should be empty, got %q", result.StderrPath)
	}
}

func TestRunCommand_HostExecution_ReturnsStderrPath(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:     dir,
		AllowPatterns: []string{"ls *"},
	}
	session := setupServerWithConfig(t, cfg)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "ls /nonexistent-path-xyz-12345"},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}

	result := parseToolResult(t, res)
	if result.ExitCode == 0 {
		t.Errorf("exit_code = 0, want non-zero (ls of nonexistent path fails)")
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
		AllowPatterns: []string{"true"},
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

func TestRunCommand_PatternMismatch_ReturnsNonZeroExitAndStderrPath(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:     dir,
		AllowPatterns: []string{"git *"}, // npm test won't match
	}
	session := setupServerWithConfig(t, cfg)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "npm test"},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}

	result := parseToolResult(t, res)
	if result.ExitCode == 0 {
		t.Error("exit_code should be non-zero for pattern mismatch")
	}
	if result.StderrPath == "" {
		t.Error("stderr_path should be non-empty for pattern mismatch")
	}
	if result.StdoutPath != "" {
		t.Errorf("stdout_path should be empty for rejection, got %q", result.StdoutPath)
	}

	// Verify rejection reason is written to the stderr file
	data, readErr := os.ReadFile(result.StderrPath)
	if readErr != nil {
		t.Fatalf("read stderr file: %v", readErr)
	}
	if !strings.Contains(string(data), "no container configured") {
		t.Errorf("stderr file should contain rejection reason, got: %q", string(data))
	}
}

func TestRunCommand_ShellOperatorRejection_ReturnsNonZeroExitAndStderrPath(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:     dir,
		AllowPatterns: []string{"git *"},
	}
	session := setupServerWithConfig(t, cfg)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "git log | head -20"},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}

	result := parseToolResult(t, res)
	if result.ExitCode == 0 {
		t.Error("exit_code should be non-zero for shell operator rejection")
	}
	if result.StderrPath == "" {
		t.Error("stderr_path should be non-empty for rejection")
	}
	if result.StdoutPath != "" {
		t.Errorf("stdout_path should be empty for rejection, got %q", result.StdoutPath)
	}

	data, readErr := os.ReadFile(result.StderrPath)
	if readErr != nil {
		t.Fatalf("read stderr file: %v", readErr)
	}
	if !strings.Contains(string(data), "rejected") {
		t.Errorf("stderr file should contain rejection reason, got: %q", string(data))
	}
}

func TestRunCommand_ContainerExecution_ReturnsExitCodeAndPaths(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:       dir,
		AllowPatterns:   []string{"git *"}, // npm test won't match → container
		ContainerRunner: &mockRunner{exitCode: 0, stdout: "test output\n"},
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
}

func TestRunCommand_ContainerRunnerError_ReturnsStructuredResponse(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:       dir,
		AllowPatterns:   []string{},
		ContainerRunner: &mockRunner{
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
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}

	result := parseToolResult(t, res)
	if result.ExitCode == 0 {
		t.Error("exit_code should be non-zero for runner error")
	}
	if result.StdoutPath == "" {
		t.Error("stdout_path should preserve partial output")
	}
	if result.StderrPath == "" {
		t.Error("stderr_path should contain runner error")
	}

	data, readErr := os.ReadFile(result.StderrPath)
	if readErr != nil {
		t.Fatalf("read stderr file: %v", readErr)
	}
	if !strings.Contains(string(data), "container exec: attach interrupted") {
		t.Errorf("stderr file should contain runner error, got: %q", string(data))
	}
}

// Ensure mockRunner satisfies the mcptool interface.
var _ mcptool.ContainerRunner = (*mockRunner)(nil)

func TestRunCommand_TimeoutSeconds_Zero_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{OutputDir: dir, AllowPatterns: []string{"echo *"}}
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
	cfg := mcptool.HandlerConfig{OutputDir: dir, AllowPatterns: []string{"echo *"}}
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

func TestRunCommand_TimeoutExpires_ReturnsExitCode124(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:     dir,
		AllowPatterns: []string{"sleep *"},
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
		AllowPatterns: []string{"echo *"},
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
		AllowPatterns: []string{"echo *"},
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

func TestRunCommand_DropPattern_WritesStderrAndExits1(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:    dir,
		DropPatterns: []string{"rm -rf *"},
	}
	session := setupServerWithConfig(t, cfg)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "rm -rf /tmp/anything"},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}

	result := parseToolResult(t, res)
	if result.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", result.ExitCode)
	}
	if result.StdoutPath != "" {
		t.Errorf("stdout_path should be empty, got %q", result.StdoutPath)
	}
	if result.StderrPath == "" {
		t.Fatal("stderr_path should be non-empty for drop")
	}

	data, readErr := os.ReadFile(result.StderrPath)
	if readErr != nil {
		t.Fatalf("read stderr file: %v", readErr)
	}
	want := "dropped: command matches drop pattern \"rm -rf *\"\n"
	if string(data) != want {
		t.Errorf("stderr file = %q, want %q", string(data), want)
	}
}

func TestRunCommand_DropPattern_DoesNotCallContainerRunner(t *testing.T) {
	dir := t.TempDir()
	// If the runner is invoked it will write the contamination strings below
	// to stdout/stderr; the assertions afterwards confirm those strings never
	// appear, proving the drop branch skipped the runner entirely.
	runner := &mockRunner{exitCode: 0, stdout: "container ran\n", stderr: "container err\n"}
	cfg := mcptool.HandlerConfig{
		OutputDir:       dir,
		DropPatterns:    []string{"rm -rf *"},
		ContainerRunner: runner,
	}
	session := setupServerWithConfig(t, cfg)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "rm -rf /tmp/anything"},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}

	result := parseToolResult(t, res)
	if result.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", result.ExitCode)
	}
	if result.StdoutPath != "" {
		t.Errorf("stdout_path should be empty (container must not run), got %q", result.StdoutPath)
	}

	data, readErr := os.ReadFile(result.StderrPath)
	if readErr != nil {
		t.Fatalf("read stderr file: %v", readErr)
	}
	want := "dropped: command matches drop pattern \"rm -rf *\"\n"
	if string(data) != want {
		t.Errorf("stderr file = %q, want %q (container runner must not contribute output)", string(data), want)
	}
}

func TestRunCommand_ContainerEnvPassthrough_PassesResolvedEnvToRunner(t *testing.T) {
	t.Setenv("CR_HANDLER_TEST_VAR", "passedvalue")

	dir := t.TempDir()
	runner := &mockRunner{exitCode: 0}
	cfg := mcptool.HandlerConfig{
		OutputDir:               dir,
		AllowPatterns:           []string{},
		ContainerRunner:         runner,
		ContainerEnvPassthrough: []string{"CR_HANDLER_TEST_VAR", "CR_HANDLER_TEST_ABSENT_XYZ"},
	}
	session := setupServerWithConfig(t, cfg)

	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "npm test"},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}

	if len(runner.capturedEnv) != 1 {
		t.Fatalf("capturedEnv len = %d, want 1 (absent key skipped)", len(runner.capturedEnv))
	}
	if runner.capturedEnv[0] != "CR_HANDLER_TEST_VAR=passedvalue" {
		t.Errorf("capturedEnv[0] = %q, want CR_HANDLER_TEST_VAR=passedvalue", runner.capturedEnv[0])
	}
}

func TestRunCommand_WithHugeTimeout_ReturnsToolError(t *testing.T) {
	dir := t.TempDir()
	cfg := mcptool.HandlerConfig{
		OutputDir:     dir,
		AllowPatterns: []string{"echo *"},
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
		AllowPatterns: []string{"echo *"},
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
