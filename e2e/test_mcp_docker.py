from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Iterator

import pytest

from e2e.conftest import (
    REPO_ROOT,
    docker_available,
    run_sandbox_command,
    start_server,
    stop_server,
)
from e2e.mcp_client import McpStdioClient


pytestmark = pytest.mark.docker


def tool_result(result: dict) -> dict:
    assert result["content"], result
    text = result["content"][0]["text"]
    return json.loads(text)


def stderr_text(body: dict) -> str:
    stderr_path = body.get("stderr_path")
    if not stderr_path:
        return ""
    return Path(stderr_path).read_text(encoding="utf-8")


@pytest.fixture()
def docker_mcp(
    agent_sandbox_binary: Path,
    docker_config: Path,
) -> Iterator[McpStdioClient]:
    if not docker_available():
        pytest.skip("Docker daemon is not available")

    up = run_sandbox_command(agent_sandbox_binary, docker_config, "sandbox-up", "-d")
    if up.returncode != 0:
        pytest.fail(
            f"sandbox-up failed with {up.returncode}\nstdout:\n{up.stdout}\nstderr:\n{up.stderr}"
        )

    process = start_server(agent_sandbox_binary, docker_config, lightweight=False)
    try:
        client = McpStdioClient(process, timeout=120)
        client.initialize()
        yield client
    finally:
        stderr = stop_server(process)
        down = run_sandbox_command(agent_sandbox_binary, docker_config, "sandbox-down")
        if down.returncode != 0:
            message = (
                f"sandbox-down failed with {down.returncode}\n"
                f"stdout:\n{down.stdout}\nstderr:\n{down.stderr}"
            )
            exc = sys.exc_info()[1]
            if exc is not None:
                exc.add_note(message)
            else:
                pytest.fail(message)
        if process.returncode not in (0, -15, None):
            exc = sys.exc_info()[1]
            message = f"server exited with {process.returncode}; stderr:\n{stderr}"
            if exc is not None:
                exc.add_note(message)
            else:
                pytest.fail(message)


def test_run_command_container_route_returns_output_file(docker_mcp: McpStdioClient) -> None:
    result = docker_mcp.call_tool("run_command", {"command": "printf container-e2e"})
    assert result.get("isError", False) is False
    body = tool_result(result)

    assert body["exit_code"] == 0, {"body": body, "stderr": stderr_text(body)}
    stdout_path = Path(body["stdout_path"])
    assert stdout_path.read_text(encoding="utf-8") == "container-e2e"


def test_fstool_tools_are_not_registered(docker_mcp: McpStdioClient) -> None:
    tools = docker_mcp.list_tools()
    names = {tool["name"] for tool in tools}
    assert "create_file" not in names
    assert "create_directory" not in names
