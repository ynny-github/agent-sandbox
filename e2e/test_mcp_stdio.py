from __future__ import annotations

import json
from pathlib import Path

from e2e.mcp_client import McpStdioClient


def tool_result(result: dict) -> dict:
    assert result["content"], result
    text = result["content"][0]["text"]
    return json.loads(text)


def test_tools_list_contains_expected_tools(lightweight_mcp: McpStdioClient) -> None:
    tools = lightweight_mcp.list_tools()
    names = {tool["name"] for tool in tools}
    assert "run_command" in names
    assert "create_file" not in names
    assert "create_directory" not in names


def test_run_command_host_route_returns_output_file(lightweight_mcp: McpStdioClient) -> None:
    result = lightweight_mcp.call_tool("run_command", {"command": "echo e2e-ok"})
    assert result.get("isError", False) is False
    body = tool_result(result)

    assert body["exit_code"] == 0
    stdout_path = Path(body["stdout_path"])
    assert stdout_path.read_text(encoding="utf-8").strip() == "e2e-ok"
    assert "stderr_path" not in body


def test_run_command_rejects_shell_operator(lightweight_mcp: McpStdioClient) -> None:
    result = lightweight_mcp.call_tool("run_command", {"command": "echo e2e-ok | cat"})
    assert result.get("isError", False) is False
    body = tool_result(result)

    assert body["exit_code"] == 1
    stderr_path = Path(body["stderr_path"])
    assert "rejected" in stderr_path.read_text(encoding="utf-8")
    assert "stdout_path" not in body


def test_run_command_invalid_timeout_returns_tool_error(lightweight_mcp: McpStdioClient) -> None:
    result = lightweight_mcp.call_tool(
        "run_command",
        {"command": "echo e2e-ok", "timeout_seconds": 0},
    )

    assert result["isError"] is True
    assert "invalid timeout_seconds" in result["content"][0]["text"]
