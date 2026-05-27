from __future__ import annotations

import json
import queue
import subprocess
import threading
import time
from dataclasses import dataclass
from typing import Any


@dataclass
class JsonRpcResponse:
    id: int
    result: dict[str, Any] | None = None
    error: dict[str, Any] | None = None


class McpStdioClient:
    def __init__(self, process: subprocess.Popen[str], timeout: float = 10.0) -> None:
        if process.stdin is None or process.stdout is None:
            raise ValueError("process must be started with stdin and stdout pipes")
        self.process = process
        self.timeout = timeout
        self._stdin = process.stdin
        self._stdout = process.stdout
        self._next_id = 1
        self._pending: dict[int, dict[str, Any]] = {}
        self._reader_error: str | None = None
        self._responses: queue.Queue[dict[str, Any] | None] = queue.Queue()
        self._reader = threading.Thread(target=self._read_stdout, daemon=True)
        self._reader.start()

    def _read_stdout(self) -> None:
        for line in self._stdout:
            stripped = line.strip()
            if not stripped:
                continue
            try:
                self._responses.put(json.loads(stripped))
            except json.JSONDecodeError as exc:
                self._reader_error = f"invalid JSON from stdout: {stripped!r}: {exc}"
                self._responses.put(None)
                return

    def request(self, method: str, params: dict[str, Any] | None = None) -> JsonRpcResponse:
        request_id = self._next_id
        self._next_id += 1
        payload: dict[str, Any] = {
            "jsonrpc": "2.0",
            "id": request_id,
            "method": method,
        }
        if params is not None:
            payload["params"] = params
        self._write(payload)

        deadline = time.monotonic() + self.timeout
        while True:
            if request_id in self._pending:
                message = self._pending.pop(request_id)
                return JsonRpcResponse(
                    id=request_id,
                    result=message.get("result"),
                    error=message.get("error"),
                )

            if self._reader_error is not None:
                raise RuntimeError(f"failed waiting for response to {method}: {self._reader_error}")

            return_code = self.process.poll()
            if return_code is not None:
                raise RuntimeError(
                    f"process exited while waiting for response to {method}: "
                    f"return code {return_code}; request: {payload}"
                )

            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError(f"timed out waiting for response to {method}: {payload}")

            try:
                message = self._responses.get(timeout=min(remaining, 0.1))
            except queue.Empty as exc:
                if time.monotonic() >= deadline:
                    raise TimeoutError(f"timed out waiting for response to {method}: {payload}") from exc
                continue

            if message is None:
                if self._reader_error is not None:
                    raise RuntimeError(f"failed waiting for response to {method}: {self._reader_error}")
                raise RuntimeError(f"failed waiting for response to {method}: stdout reader stopped")

            if message.get("id") != request_id:
                message_id = message.get("id")
                if isinstance(message_id, int):
                    self._pending[message_id] = message
                continue
            return JsonRpcResponse(
                id=request_id,
                result=message.get("result"),
                error=message.get("error"),
            )

    def notify(self, method: str, params: dict[str, Any] | None = None) -> None:
        payload: dict[str, Any] = {
            "jsonrpc": "2.0",
            "method": method,
        }
        if params is not None:
            payload["params"] = params
        self._write(payload)

    def initialize(self) -> dict[str, Any]:
        response = self.request(
            "initialize",
            {
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": {"name": "agent-sandbox-e2e", "version": "0.1.0"},
            },
        )
        if response.error is not None:
            raise AssertionError(f"initialize returned error: {response.error}")
        if response.result is None:
            raise AssertionError("initialize returned no result")
        self.notify("notifications/initialized")
        return response.result

    def list_tools(self) -> list[dict[str, Any]]:
        response = self.request("tools/list", {})
        if response.error is not None:
            raise AssertionError(f"tools/list returned error: {response.error}")
        if response.result is None:
            raise AssertionError("tools/list returned no result")
        if "tools" not in response.result:
            raise AssertionError(f"tools/list returned result without tools: {response.result}")
        return response.result["tools"]

    def call_tool(self, name: str, arguments: dict[str, Any]) -> dict[str, Any]:
        response = self.request("tools/call", {"name": name, "arguments": arguments})
        if response.error is not None:
            raise AssertionError(f"tools/call returned protocol error: {response.error}")
        if response.result is None:
            raise AssertionError("tools/call returned no result")
        return response.result

    def _write(self, payload: dict[str, Any]) -> None:
        self._stdin.write(json.dumps(payload, separators=(",", ":")) + "\n")
        self._stdin.flush()
