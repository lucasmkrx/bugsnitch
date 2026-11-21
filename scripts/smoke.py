#!/usr/bin/env python3
"""Destructive tests are confined to two newly created databases and temporary runner homes."""
import contextlib
import http.cookiejar
import json
import os
import secrets
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
BIN = ROOT / "bin"
DB = "bugsnitch_smoke_" + secrets.token_hex(6)
RESTORE = DB + "_restore"


def run(*args, env=None, cwd=None):
    return subprocess.run(args, env=env, cwd=cwd, text=True, capture_output=True, check=True).stdout


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


@contextlib.contextmanager
def server(env):
    proc = subprocess.Popen([BIN / "bugsnitch-server"], env=env, stdout=subprocess.DEVNULL,
                            stderr=subprocess.PIPE, cwd=ROOT)
    try:
        until = time.monotonic() + 15
        while time.monotonic() < until:
            if proc.poll() is not None:
                raise RuntimeError(proc.stderr.read(2000).decode())
            try:
                with urllib.request.urlopen(env["BUGSNITCH_ORIGIN"] + "/readyz", timeout=1) as response:
                    assert json.load(response)["status"] == "ready"
                break
            except OSError:
                time.sleep(0.1)
        else:
            raise RuntimeError("service readiness timed out")
        yield
    finally:
        proc.terminate()
        proc.wait(timeout=12)
        proc.stderr.close()


def check(tmp):
    base = os.environ.copy()
    base.update(BUGSNITCH_OWNER_TOKEN=secrets.token_hex(32), BUGSNITCH_MCP_TOKEN=secrets.token_hex(32),
                DATABASE_URL=f"postgresql:///{DB}?sslmode=disable")
    base["BUGSNITCH_LISTEN"] = f"127.0.0.1:{port()}"
    base["BUGSNITCH_ORIGIN"] = "http://" + base["BUGSNITCH_LISTEN"]
    origin = base["BUGSNITCH_ORIGIN"]
    browser = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def api(path, data=None):
        req = urllib.request.Request(origin + "/api/v1/" + path,
                                     data=json.dumps(data).encode() if data is not None else None,
                                     headers={"Content-Type": "application/json", "Origin": origin})
        with browser.open(req, timeout=10) as response:
            return json.load(response)

    checkout = tmp / "checkout"
    run("git", "init", "-q", str(checkout))
    run("git", "-C", str(checkout), "remote", "add", "origin", "https://github.com/example/fixture.git")
    homes = []
    with server(base):
        with browser.open(origin) as response:
            assert 'id="root"' in response.read().decode()
        api("login", {"token": base["BUGSNITCH_OWNER_TOKEN"]})
        project = api("projects", {"name": "Smoke project", "repository": "example/fixture"})["id"]
        for name in ("one", "two"):
            grant = api("pairings", {"name": name, "projectIds": [project]})
            local = {**base, "BUGSNITCH_HOME": str(tmp / name)}
            run(BIN / "bugsnitch", "pair", "--server", origin, "--code", grant["code"], env=local)
            run(BIN / "bugsnitch", "register", "--project", project, "--path", checkout, env=local)
            run(BIN / "bugsnitch", "sync", env=local)
            homes.append(local)
        state = api("state")
        assert len({d["id"] for d in state["devices"]}) == 2
        print("Two independent runner processes paired, registered the fixture repository and synchronized.")
        call = lambda i, method, params: {"jsonrpc": "2.0", "id": i, "method": method, "params": params}
        messages = [
            call(1, "initialize", {"protocolVersion": "2025-11-25", "capabilities": {},
                                   "clientInfo": {"name": "smoke", "version": "1"}}),
            {"jsonrpc": "2.0", "method": "notifications/initialized"},
            call(2, "tools/call", {"name": "list_projects", "arguments": {}}),
            call(3, "tools/call", {"name": "report_incident", "arguments": {
                "projectId": project, "requestId": "smoke_report_001", "observed": "Preview is blank",
                "expected": "A preview", "steps": "Open preview"}})
        ]
        # Wait for each response before issuing a dependent message or closing stdin.
        with subprocess.Popen([BIN / "bugsnitch-mcp"], env=base, stdin=subprocess.PIPE,
                              stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True) as mcp:
            for message in messages:
                mcp.stdin.write(json.dumps(message) + "\n")
                mcp.stdin.flush()
                if "id" in message:
                    reply = json.loads(mcp.stdout.readline())
                    assert reply["id"] == message["id"] and "error" not in reply and not reply["result"].get("isError")
                    if reply["id"] == 2:
                        assert project in json.dumps(reply["result"])
            mcp.stdin.close()
            mcp.wait(timeout=10)
        assert len(api("state")["incidents"]) == 1
        print("Local MCP STDIO and hosted streamable HTTP observed dashboard data and recorded one incident.")
    with server(base):
        assert len(api("state")["devices"]) == 2
        for local in homes:
            run(BIN / "bugsnitch", "sync", env=local)
        assert len(api("state")["devices"]) == 2
        dump = tmp / "backup.dump"
        run("pg_dump", "-Fc", "--no-owner", "-f", dump, DB)
    run("pg_restore", "--exit-on-error", "--no-owner", "-d", RESTORE, dump)
    restored = {**base, "DATABASE_URL": f"postgresql:///{RESTORE}?sslmode=disable"}
    with server(restored):
        state = api("state")
        assert len(state["devices"]) == 2 and len(state["incidents"]) == 1 and state["projects"][0]["id"] == project
        revoke = next(d["id"] for d in state["devices"] if d["name"] == "one")
        api(f"devices/{revoke}/revoke", {})
        proc = subprocess.run([BIN / "bugsnitch", "sync"], env=homes[0], capture_output=True)
        assert proc.returncode and b"403" in proc.stderr
        run(BIN / "bugsnitch", "sync", env=homes[1])
    print("Restart, migration replay, backup restore, session continuity, runner reconnect and revocation passed.")


if __name__ == "__main__":
    if not os.getenv("PGHOST"):
        raise SystemExit("Set PGHOST, PGPORT and PGUSER for a disposable PostgreSQL instance with CREATEDB.")
    with contextlib.ExitStack() as cleanup:
        for database in (DB, RESTORE):
            run("createdb", database)
            cleanup.callback(run, "dropdb", database)
        with tempfile.TemporaryDirectory() as tmp:
            check(Path(tmp))
