from __future__ import annotations

from e2e.mcp_client import McpStdioClient


def test_tools_list_contains_expected_tools(lightweight_mcp: McpStdioClient) -> None:
    tools = lightweight_mcp.list_tools()
    names = {tool["name"] for tool in tools}
    assert "run_command" in names
    assert "create_file" not in names
    assert "create_directory" not in names


def test_run_command_without_broker_returns_hint(lightweight_mcp: McpStdioClient) -> None:
    # There is no host route left at all: every command now goes through the
    # broker (internal/broker), and the lightweight server this fixture starts
    # (AGENT_SANDBOX_E2E_LIGHTWEIGHT=1) wires up no CommandRunner, on purpose —
    # it exists so the MCP server can still start without a live nono session.
    # `run_command` must therefore fail the same way for *every* command,
    # including one as trivial as `echo`, with the actionable hint rather than
    # a raw connection error (see mcptool.HandleRunCommand's nil-CommandRunner
    # guard and broker.SandboxNotRunningHint).
    result = lightweight_mcp.call_tool("run_command", {"command": "echo e2e-ok"})
    assert result.get("isError") is True
    text = result["content"][0]["text"]
    assert "command broker is not available" in text


def test_run_command_invalid_timeout_returns_tool_error(lightweight_mcp: McpStdioClient) -> None:
    result = lightweight_mcp.call_tool(
        "run_command",
        {"command": "echo e2e-ok", "timeout_seconds": 0},
    )

    assert result["isError"] is True
    assert "invalid timeout_seconds" in result["content"][0]["text"]
