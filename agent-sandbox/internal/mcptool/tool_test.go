package mcptool_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ynny-github/agent-sandbox/agent-sandbox/internal/mcptool"
)

func setupTestServer(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	mcptool.Register(server, mcptool.HandlerConfig{OutputDir: t.TempDir()})

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

func TestRegister_ListTools_ContainsRunCommand(t *testing.T) {
	session := setupTestServer(t)
	res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	for _, tool := range res.Tools {
		if tool.Name == "run_command" {
			return
		}
	}
	t.Errorf("run_command not found in tool list: %v", res.Tools)
}

func TestRegister_RunCommand_HasCommandParameter(t *testing.T) {
	session := setupTestServer(t)
	res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "run_command" {
			continue
		}
		if tool.InputSchema == nil {
			t.Fatal("run_command has nil InputSchema")
		}
		schema := schemaMap(t, tool.InputSchema)
		properties := nestedMap(t, schema, "properties")
		command := nestedMap(t, properties, "command")
		if command["type"] != "string" {
			t.Fatalf("command schema type = %v, want string", command["type"])
		}
		if !requiredContains(schema, "command") {
			t.Fatalf("run_command required fields = %v, want command", schema["required"])
		}
		return
	}
	t.Error("run_command not found")
}

func TestRegister_RunCommand_HasOptionalTimeoutSecondsParameter(t *testing.T) {
	session := setupTestServer(t)
	res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "run_command" {
			continue
		}
		schema := schemaMap(t, tool.InputSchema)
		properties := nestedMap(t, schema, "properties")
		if _, ok := properties["timeout_seconds"]; !ok {
			t.Error("run_command schema should have timeout_seconds property")
		}
		if requiredContains(schema, "timeout_seconds") {
			t.Error("timeout_seconds should not be in required fields")
		}
		return
	}
	t.Error("run_command not found")
}

func TestRegister_CallRunCommand_ReturnsStructuredResponse(t *testing.T) {
	session := setupTestServer(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_command",
		Arguments: map[string]any{"command": "echo hi"},
	})
	if err != nil {
		t.Fatalf("CallTool protocol error: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil CallToolResult")
	}
}

func schemaMap(t *testing.T, schema any) map[string]any {
	t.Helper()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	return decoded
}

func nestedMap(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key]
	if !ok {
		t.Fatalf("schema missing %q in %v", key, parent)
	}
	nested, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("schema %q = %T, want object", key, value)
	}
	return nested
}

func requiredContains(schema map[string]any, field string) bool {
	required, ok := schema["required"].([]any)
	if !ok {
		return false
	}
	for _, item := range required {
		if item == field {
			return true
		}
	}
	return false
}
