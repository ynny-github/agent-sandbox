from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Iterator

import pytest


REPO_ROOT = Path(__file__).resolve().parents[1]
GO_MODULE_DIR = REPO_ROOT / "agent-sandbox"

if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))

from e2e.mcp_client import McpStdioClient


_STDERR_PATH_ATTR = "_agent_sandbox_e2e_stderr_path"


def toml_string(value: str | Path) -> str:
    return json.dumps(str(value))


@pytest.fixture(scope="session")
def agent_sandbox_binary(tmp_path_factory: pytest.TempPathFactory) -> Path:
    output = tmp_path_factory.mktemp("bin") / "agent-sandbox-e2e"
    subprocess.run(
        ["go", "build", "-o", str(output), "."],
        cwd=GO_MODULE_DIR,
        check=True,
        timeout=120,
    )
    return output


@pytest.fixture()
def output_dir(tmp_path: Path) -> Path:
    path = tmp_path / "mcp-output"
    path.mkdir()
    return path


@pytest.fixture()
def lightweight_config(tmp_path: Path, output_dir: Path) -> Path:
    config = tmp_path / "agent-sandbox.toml"
    config.write_text(
        f"""
[server]
output_dir = {toml_string(output_dir)}

[sandbox]
build_context = {toml_string("./docker/sandbox")}
dockerfile = "Dockerfile"
image = "e2e"
allow_cidrs = []
allow_hosts = []
external_network = ""

[allow_patterns]
patterns = ["echo *", "printf *"]

[deny_patterns]
patterns = []

[container]
env_passthrough = []
""".strip()
        + "\n",
        encoding="utf-8",
    )
    return config


@pytest.fixture()
def docker_config(tmp_path: Path, output_dir: Path) -> Path:
    nono_output_dir = tmp_path / "nono-output"
    nono_output_dir.mkdir()

    config = tmp_path / "agent-sandbox.toml"
    config.write_text(
        f"""
[server]
output_dir = {toml_string(output_dir)}

[sandbox]
build_context = {toml_string(REPO_ROOT / "docker" / "sandbox")}
dockerfile = "Dockerfile"
image = "e2e"
allow_cidrs = []
allow_hosts = []
external_network = ""

[allow_patterns]
patterns = ["echo host-*"]

[deny_patterns]
patterns = []

[container]
env_passthrough = []

[nono]
config = {toml_string(REPO_ROOT / "nono.toml")}
yolo_config = {toml_string(REPO_ROOT / "nono-yolo.toml")}
output_dir = {toml_string(nono_output_dir)}
""".strip()
        + "\n",
        encoding="utf-8",
    )
    return config


def start_server(
    binary: Path,
    config: Path,
    *,
    lightweight: bool,
) -> subprocess.Popen[str]:
    env = os.environ.copy()
    if lightweight:
        env["AGENT_SANDBOX_E2E_LIGHTWEIGHT"] = "1"
    else:
        env.pop("AGENT_SANDBOX_E2E_LIGHTWEIGHT", None)

    stderr_file = tempfile.NamedTemporaryFile(
        mode="w",
        encoding="utf-8",
        prefix="agent-sandbox-e2e-stderr-",
        suffix=".log",
        delete=False,
    )
    try:
        process = subprocess.Popen(
            [str(binary), "command-router", "--config", str(config)],
            cwd=REPO_ROOT,
            env=env,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=stderr_file,
            text=True,
            bufsize=1,
        )
    except Exception:
        stderr_file.close()
        Path(stderr_file.name).unlink(missing_ok=True)
        raise
    stderr_file.close()
    setattr(process, _STDERR_PATH_ATTR, Path(stderr_file.name))
    return process


def run_sandbox_command(binary: Path, config: Path, *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [str(binary), *args, "--config", str(config)],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=180,
    )


def stop_server(process: subprocess.Popen[str]) -> str:
    if process.poll() is None:
        process.terminate()
        try:
            process.communicate(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.communicate(timeout=10)
    else:
        process.communicate(timeout=10)

    stderr_path = getattr(process, _STDERR_PATH_ATTR, None)
    if not isinstance(stderr_path, Path):
        return ""
    try:
        return stderr_path.read_text(encoding="utf-8")
    except FileNotFoundError:
        return ""
    finally:
        stderr_path.unlink(missing_ok=True)


@pytest.fixture()
def lightweight_mcp(
    agent_sandbox_binary: Path,
    lightweight_config: Path,
) -> Iterator[McpStdioClient]:
    process = start_server(agent_sandbox_binary, lightweight_config, lightweight=True)
    try:
        client = McpStdioClient(process)
        client.initialize()
        yield client
    finally:
        stderr = stop_server(process)
        if process.returncode not in (0, -15, None):
            exc = sys.exc_info()[1]
            message = f"server exited with {process.returncode}; stderr:\n{stderr}"
            if exc is not None:
                exc.add_note(message)
            else:
                pytest.fail(message)


def docker_available() -> bool:
    try:
        result = subprocess.run(
            ["docker", "info"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            timeout=10,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return False
    return result.returncode == 0
