#!/usr/bin/env python3
"""Diagnose activesync-mcp through its real MCP stdio interface.

No password is accepted or printed. activesync-mcp resolves the secret from
the keyring or command configured in config.toml.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import queue
import subprocess
import sys
import threading
from typing import Any, TextIO


PROTOCOL_VERSION = "2025-11-25"


class MCPError(RuntimeError):
    """An MCP transport, protocol, or tool error."""


def _readline_with_timeout(stream: TextIO, timeout: float) -> str:
    result: queue.Queue[object] = queue.Queue(maxsize=1)

    def read() -> None:
        try:
            result.put(stream.readline())
        except BaseException as exc:
            result.put(exc)

    threading.Thread(target=read, daemon=True).start()
    try:
        value = result.get(timeout=timeout)
    except queue.Empty as exc:
        raise MCPError(f"timeout waiting for MCP response ({timeout:g}s)") from exc
    if isinstance(value, BaseException):
        raise MCPError(f"cannot read MCP response: {value}") from value
    return str(value)


class MCPConnection:
    """Small synchronous client for MCP's newline-delimited JSON transport."""

    def __init__(self, reader: TextIO, writer: TextIO, timeout: float = 45) -> None:
        self.reader = reader
        self.writer = writer
        self.timeout = timeout
        self.next_id = 1

    def _send(self, message: dict[str, Any]) -> None:
        self.writer.write(json.dumps(message, ensure_ascii=False) + "\n")
        self.writer.flush()

    def notify(self, method: str, params: dict[str, Any] | None = None) -> None:
        self._send({"jsonrpc": "2.0", "method": method, "params": params or {}})

    def request(self, method: str, params: dict[str, Any] | None = None) -> Any:
        request_id = self.next_id
        self.next_id += 1
        self._send({
            "jsonrpc": "2.0",
            "id": request_id,
            "method": method,
            "params": params or {},
        })

        while True:
            line = _readline_with_timeout(self.reader, self.timeout)
            if not line:
                raise MCPError("server closed stdout before sending an MCP response")
            try:
                message = json.loads(line)
            except json.JSONDecodeError as exc:
                raise MCPError(f"server wrote non-JSON data to stdout: {line.rstrip()!r}") from exc
            if message.get("id") != request_id:
                continue
            if "error" in message:
                error = message["error"]
                raise MCPError(f"JSON-RPC {error.get('code')}: {error.get('message')}")
            if "result" not in message:
                raise MCPError(f"malformed MCP response: {message!r}")
            return message["result"]


def ensure_tool_success(result: dict[str, Any]) -> dict[str, Any]:
    if not result.get("isError"):
        return result
    texts = [item.get("text", "") for item in result.get("content", [])
             if item.get("type") == "text"]
    detail = "\n".join(filter(None, texts)) or repr(result)
    raise MCPError(f"MCP tool returned an error: {detail}")


def default_config_path() -> Path:
    appdata = os.environ.get("APPDATA")
    if appdata:
        return Path(appdata) / "activesync-mcp" / "config.toml"
    return Path.home() / ".config" / "activesync-mcp" / "config.toml"


def text_content(result: dict[str, Any]) -> str:
    return "\n".join(item.get("text", "") for item in result.get("content", [])
                     if item.get("type") == "text")


def tool_payload(result: dict[str, Any]) -> dict[str, Any]:
    text = text_content(result)
    if not text:
        raise MCPError("MCP tool returned no JSON text content")
    try:
        payload = json.loads(text)
    except json.JSONDecodeError as exc:
        raise MCPError(f"MCP tool returned invalid JSON text: {exc}") from exc
    if not isinstance(payload, dict):
        raise MCPError("MCP tool JSON result is not an object")
    return payload


def drain_stderr(stream: TextIO, lines: list[str]) -> None:
    for line in stream:
        lines.append(line.rstrip())


def run_diagnostic(exe: Path, config: Path, account: str, timeout: float) -> int:
    print(f"Executable: {exe}")
    print(f"Config:     {config}")
    print(f"Account:    {account}")
    print("Password:   resolved by activesync-mcp (not exposed)\n")

    process = subprocess.Popen(
        [str(exe), "serve", "--config", str(config)],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, encoding="utf-8", bufsize=1,
    )
    assert process.stdin is not None and process.stdout is not None
    assert process.stderr is not None
    stderr_lines: list[str] = []
    threading.Thread(target=drain_stderr, args=(process.stderr, stderr_lines),
                     daemon=True).start()
    connection = MCPConnection(process.stdout, process.stdin, timeout)

    try:
        print("[1/7] initialize", flush=True)
        initialized = connection.request("initialize", {
            "protocolVersion": PROTOCOL_VERSION,
            "capabilities": {},
            "clientInfo": {"name": "eas-check", "version": "1.0"},
        })
        server = initialized.get("serverInfo", {})
        print(f"      OK: {server.get('name', '?')} {server.get('version', '?')}")
        connection.notify("notifications/initialized")

        print("[2/7] tools/list", flush=True)
        listed = connection.request("tools/list")
        names = [tool.get("name", "?") for tool in listed.get("tools", [])]
        print(f"      OK: {len(names)} tools")
        for name in names:
            print(f"      - {name}")
        if "accounts_list" not in names or "email_list_folders" not in names:
            raise MCPError("required tools accounts_list/email_list_folders are not registered")

        print("[3/7] accounts_list", flush=True)
        accounts = ensure_tool_success(connection.request("tools/call", {
            "name": "accounts_list", "arguments": {},
        }))
        print("      OK")
        configured = tool_payload(accounts).get("accounts", [])
        print(f"      configured accounts: {len(configured)}")

        print("[4/7] email_list_folders (Provision + FolderSync)", flush=True)
        folders = ensure_tool_success(connection.request("tools/call", {
            "name": "email_list_folders", "arguments": {"account": account},
        }))
        print("      OK: Provision and FolderSync completed")
        email_folders = tool_payload(folders).get("folders", [])
        inbox = next((folder for folder in email_folders
                      if folder.get("type") == "Inbox"), None)
        if inbox is None:
            raise MCPError("email_list_folders returned no Inbox")
        print(f"      email folders: {len(email_folders)}; Inbox id: {inbox['id']}")

        print("[5/7] email_list (bootstrap + item Sync)", flush=True)
        email_result = ensure_tool_success(connection.request("tools/call", {
            "name": "email_list",
            "arguments": {"account": account, "folder_id": inbox["id"],
                          "window_size": 1, "date_window": "1d",
                          "body_preview_bytes": 0},
        }))
        email_payload = tool_payload(email_result)
        print(f"      OK: {len(email_payload.get('items', []))} item(s) returned")

        print("[6/7] calendar_list_folders", flush=True)
        calendar_folders_result = ensure_tool_success(connection.request("tools/call", {
            "name": "calendar_list_folders", "arguments": {"account": account},
        }))
        calendar_folders = tool_payload(calendar_folders_result).get("folders", [])
        if not calendar_folders:
            raise MCPError("calendar_list_folders returned no calendar")
        calendar = calendar_folders[0]
        print(f"      OK: {len(calendar_folders)} folder(s); calendar id: {calendar['id']}")

        print("[7/7] calendar_list_events (bootstrap + item Sync)", flush=True)
        events_result = ensure_tool_success(connection.request("tools/call", {
            "name": "calendar_list_events",
            "arguments": {"account": account, "folder_id": calendar["id"],
                          "window_size": 1, "date_window": "1m"},
        }))
        events_payload = tool_payload(events_result)
        print(f"      OK: {len(events_payload.get('events', []))} event(s) returned")
        return 0
    except (MCPError, OSError) as exc:
        print(f"\nFAIL: {exc}", file=sys.stderr)
        if stderr_lines:
            print("\nactivesync-mcp stderr:", file=sys.stderr)
            for line in stderr_lines:
                print(f"  {line}", file=sys.stderr)
        return 1
    finally:
        try:
            process.stdin.close()
        except OSError:
            pass
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            process.terminate()
            try:
                process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                process.kill()


def main() -> int:
    script_dir = Path(__file__).resolve().parent
    parser = argparse.ArgumentParser(
        description="Test activesync-mcp through MCP, including Provision and FolderSync")
    parser.add_argument("--exe", type=Path, default=script_dir / "activesync-mcp.exe")
    parser.add_argument("--config", type=Path, default=default_config_path())
    parser.add_argument("--account", default="work")
    parser.add_argument("--timeout", type=float, default=45)
    args = parser.parse_args()

    if not args.exe.is_file():
        parser.error(f"executable not found: {args.exe}")
    if not args.config.is_file():
        parser.error(f"config not found: {args.config}")
    if args.timeout <= 0:
        parser.error("--timeout must be greater than zero")
    return run_diagnostic(args.exe, args.config, args.account, args.timeout)


if __name__ == "__main__":
    sys.exit(main())
