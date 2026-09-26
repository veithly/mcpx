#!/usr/bin/env python3
"""Smoke-test MCPX's native programming tools over HTTP in temporary state.

Requires Python 3 and macOS/Linux. The server gets a minimal PATH with system
utilities and failing traps for codex, codex-exec-server and apply_patch. No real
Codex lookup, model/API call, install, or resident-service operation is performed.
"""
import argparse
import json
import os
from pathlib import Path
import shlex
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


class MCPClient:
    def __init__(self, endpoint):
        self.endpoint = endpoint
        self.counter = 0
        self.headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
        self.http = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def rpc(self, method, params, notification=False):
        body = {"jsonrpc": "2.0", "method": method, "params": params}
        if not notification:
            self.counter += 1
            body["id"] = self.counter
        request = urllib.request.Request(self.endpoint, data=json.dumps(body).encode(), headers=self.headers)
        with self.http.open(request, timeout=35) as response:
            session = response.headers.get("Mcp-Session-Id")
            if session:
                self.headers["Mcp-Session-Id"] = session
            if notification:
                return None
            if response.headers.get_content_type() == "text/event-stream":
                lines = []
                for line in response:
                    line = line.decode().rstrip("\r\n")
                    if line.startswith("data:"):
                        lines.append(line[5:].lstrip())
                    elif not line and lines:
                        message = json.loads("\n".join(lines))
                        lines = []
                        if message.get("id") == body["id"]:
                            break
                else:
                    raise RuntimeError("MCP stream ended before its RPC result")
            else:
                message = json.load(response)
        require(message.get("id") == body["id"], "MCP response ID mismatch")
        require("error" not in message, f"{method} RPC error: {message.get('error')}")
        return message["result"]

    def call(self, tool, arguments, expect_error=False):
        result = self.rpc("tools/call", {"name": tool, "arguments": arguments})
        require(bool(result.get("isError", False)) == expect_error,
                f"{tool}: unexpected error state: {result.get('structuredContent')}")
        data = result.get("structuredContent")
        require(isinstance(data, dict), f"{tool}: missing structured result")
        if tool in {"exec_command", "write_stdin", "apply_patch"}:
            require("data" not in data and "status" not in data, f"{tool}: programming result gained an ARC envelope")
            require(isinstance(data.get("output"), str), f"{tool}: missing output")
        return data


def isolated_environment(root):
    command_dir = root / "bin"
    command_dir.mkdir()
    # Only these host utilities are reachable by name; never inherit the user's PATH.
    for name in ("sh", "cat", "sleep", "stty", "touch"):
        executable = next((base / name for base in (Path("/bin"), Path("/usr/bin"))
                           if (base / name).is_file() and os.access(base / name, os.X_OK)), None)
        require(executable is not None, f"Required system utility unavailable: {name}")
        (command_dir / name).symlink_to(executable)
    marker = root / "unexpected-external-tool.log"
    for name in ("codex", "codex-exec-server", "apply_patch"):
        trap = command_dir / name
        trap.write_text("#!/bin/sh\nprintf '%s\\n' " + shlex.quote(name) + " >> "
                        + shlex.quote(str(marker)) + "\nexit 97\n", encoding="utf-8")
        trap.chmod(0o755)
    env = {key: os.environ[key] for key in ("LANG", "LC_ALL") if key in os.environ}
    env.update(PATH=str(command_dir), HOME=str(root / "user"), MCPX_HOME=str(root / "mcpx"),
               TMPDIR=str(root / "tmp"), XDG_CONFIG_HOME=str(root / "config"),
               XDG_DATA_HOME=str(root / "data"), XDG_CACHE_HOME=str(root / "cache"), SHELL="/bin/sh")
    return env, marker


def smoke(binary, output, checks):
    # Keep the observer Unix socket below macOS/Linux sun_path limits.
    with tempfile.TemporaryDirectory(prefix="mcpx-toolchain-", dir="/tmp") as temp:
        root = Path(temp).resolve()
        project = root / "project"
        home = root / "mcpx"
        for directory in (project, home, root / "user", root / "skills", root / "tmp"):
            directory.mkdir()
        config = {
            "auth": {"mode": "open"},
            "security": {"commands": {"default": "allow", "allow": [], "confirm": [], "deny": ["^touch forbidden"]}},
            "workspaces": [{"name": "probe", "path": str(project)}],
            "logging": {"enabled": False},
            "file_watch": {"enabled": False},
            "discovery": {"mcp": {"enabled": False}, "skills": {"enabled": False, "dirs": [str(root / "skills")]},
                          "instructions": {"global_agents_path": str(root / "AGENTS.md")}},
        }
        # JSON is valid YAML; avoid a dependency and quote filesystem paths safely.
        (home / "config.yaml").write_text(json.dumps(config), encoding="utf-8")
        env, external_marker = isolated_environment(root)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        with (output / "probe-server.log").open("w", encoding="utf-8") as log:
            proc = subprocess.Popen([binary, "-addr", f"127.0.0.1:{port}"], cwd=project, env=env,
                                    stdout=log, stderr=log, start_new_session=True)
            try:
                deadline = time.monotonic() + 15
                while True:
                    require(proc.poll() is None, "Probe server exited; inspect probe-server.log")
                    try:
                        with socket.create_connection(("127.0.0.1", port), timeout=0.5):
                            break
                    except OSError:
                        require(time.monotonic() < deadline, "Probe server did not listen")
                        time.sleep(0.2)
                client = MCPClient(f"http://127.0.0.1:{port}/mcp")
                initialized = client.rpc("initialize", {"protocolVersion": "2025-03-26", "capabilities": {},
                    "clientInfo": {"name": "toolchain-probe", "version": "1"}})
                client.headers["MCP-Protocol-Version"] = initialized["protocolVersion"]
                client.rpc("notifications/initialized", {}, notification=True)
                catalog = {tool["name"]: tool for tool in client.rpc("tools/list", {})["tools"]}
                require(not {"read", "edit", "execute"}.intersection(catalog), "Retired programming tools are public")
                for name, required in (("exec_command", "cmd"), ("write_stdin", "session_id"), ("apply_patch", "input")):
                    require(name in catalog, f"Missing {name}")
                    schema = catalog[name]["inputSchema"]
                    require(schema.get("required") == [required], f"{name}: wrong required arguments")
                    require(not {"purpose", "user_confirmed", "sandbox_permissions", "execution_mode", "idempotency_key"}.intersection(schema["properties"]),
                            f"{name}: unsupported programming parameters")
                require(catalog["write_stdin"]["inputSchema"]["properties"]["session_id"]["type"] == "integer", "Process handle must be integer")
                checks.append("HTTP initialize and new programming catalog")
                opened = client.call("session", {"workspace": "probe", "label": "isolated native toolchain smoke"})
                remote = opened["data"]["remote_session"]["id"]

                def call(tool, arguments, expect_error=False):
                    return client.call(tool, dict(arguments, remote_session_id=remote), expect_error)

                def run(cmd, **options):
                    args = {"cmd": cmd, "shell": "/bin/sh", "login": False, "yield_time_ms": 250, "max_output_tokens": 2000}
                    args.update(options)
                    return call("exec_command", args)

                def finish(data):
                    chunks = [data["output"]]
                    deadline = time.monotonic() + 15
                    while "exit_code" not in data:
                        require(time.monotonic() < deadline, "Process did not finish within smoke deadline")
                        require(type(data.get("session_id")) is int, "Missing integer process handle")
                        data = call("write_stdin", {"session_id": data["session_id"], "yield_time_ms": 1000, "max_output_tokens": 2000})
                        chunks.append(data["output"])
                    return "".join(chunks), data["exit_code"]

                def patch(body):
                    result = call("apply_patch", {"input": "*** Begin Patch\n" + body + "\n*** End Patch"})
                    require(result.get("exit_code") == 0, "Native patch did not succeed")

                patch("*** Add File: demo.txt\n+before")
                require(finish(run("cat demo.txt")) == ("before\n", 0), "Native source read failed")
                patch("*** Update File: demo.txt\n@@\n-before\n+after")
                require(finish(run("cat demo.txt")) == ("after\n", 0), "Repeated command replayed stale output")
                require((project / "demo.txt").read_bytes() == b"after\n", "Patch bytes were not written")
                checks.append("native patch add/update and fresh command reads")
                text, code = finish(run("printf stdout-marker; printf stderr-marker >&2; exit 7"))
                require(code == 7 and text.count("stdout-marker") == 1 and text.count("stderr-marker") == 1,
                        f"Combined output or nonzero exit changed: code={code}, output={text!r}")
                checks.append("combined output and nonzero exit remain command results")
                first = run("printf first; sleep 1; printf second")
                require(type(first.get("session_id")) is int, "Long command did not yield a process handle")
                require(finish(first) == ("firstsecond", 0), "Polling lost or duplicated output")
                checks.append("write_stdin process continuation")
                interactive = run("stty -echo; printf ready; read value; printf 'received:%s' \"$value\"", tty=True)
                require(type(interactive.get("session_id")) is int, "PTY did not return a process handle")
                reply = call("write_stdin", {"session_id": interactive["session_id"], "chars": "hello\n", "yield_time_ms": 1000})
                text, code = finish(reply)
                require(code == 0 and "received:hello" in interactive["output"] + text, "PTY input did not reach native process")
                checks.append("PTY stdin interaction")
                patch("*** Update File: demo.txt\n*** Move to: moved.txt\n@@\n-after\n+done")
                require(not (project / "demo.txt").exists() and (project / "moved.txt").read_bytes() == b"done\n", "Patch move failed")
                patch("*** Delete File: moved.txt")
                require(not (project / "moved.txt").exists(), "Patch delete failed")
                bad_patch = {"input": "*** Begin Patch\n*** Update File: missing.txt\n@@\n-no\n+yes\n*** End Patch"}
                call("apply_patch", bad_patch, expect_error=True)
                checks.append("native patch move/delete and failure")
                call("exec_command", {"cmd": "printf escape", "workdir": str(root),
                                      "shell": "/bin/sh", "login": False}, expect_error=True)
                call("apply_patch", {"input": "*** Begin Patch\n*** Add File: ../escape.txt\n+escape\n*** End Patch"}, expect_error=True)
                call("exec_command", {"cmd": "touch forbidden", "shell": "/bin/sh", "login": False}, expect_error=True)
                require(not (root / "escape.txt").exists() and not (project / "forbidden").exists(), "Rejected operation changed files")
                checks.append("Workspace boundary and configured command denial")
                client.call("session", {"remote_session_id": remote, "mode": "closed"})
            finally:
                if proc.poll() is None:
                    os.killpg(proc.pid, signal.SIGTERM)
                    try:
                        proc.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        os.killpg(proc.pid, signal.SIGKILL)
                        proc.wait(timeout=5)
                if external_marker.exists():
                    (output / "unexpected-external-tool.log").write_bytes(external_marker.read_bytes())
                    raise RuntimeError("Native smoke invoked an external-tool trap; inspect unexpected-external-tool.log")
            checks.append("minimal PATH: no codex/codex-exec-server/apply_patch invocation during startup, HTTP tools or shutdown")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, help="Built MCPX candidate binary; never installed by this probe")
    parser.add_argument("--output-dir", required=True, help="Directory for probe-server.log and probe-result.json")
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=True)
    checks = []
    report = {"result": "failed", "backend": "mcpx-native", "platform": sys.platform,
              "checks": checks, "isolation": "temporary HOME/MCPX_HOME and minimal PATH with external-tool traps"}
    try:
        require(sys.platform == "darwin" or sys.platform.startswith("linux"),
                "This smoke requires macOS/Linux; unsupported platforms fail rather than skip")
        binary = str(Path(args.binary).resolve())
        require(os.path.isfile(binary) and os.access(binary, os.X_OK), "MCPX candidate is not executable")
        report["binary"] = binary
        smoke(binary, output, checks)
        report["result"] = "passed"
    except Exception as error:
        report["error"] = str(error)
    (output / "probe-result.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report, ensure_ascii=False))
    return 0 if report["result"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
