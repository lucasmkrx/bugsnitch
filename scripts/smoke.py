#!/usr/bin/env python3
"""Destructive tests are confined to two newly created databases and temporary runner homes."""
import argparse
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
def server(env, executable=None):
    proc = subprocess.Popen([executable or BIN / "bugsnitch-server"], env=env, stdout=subprocess.DEVNULL,
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


def check(tmp, mcp_bridge, runner, initial_server=None):
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

    def exercise_interfaces(project):
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
        with subprocess.Popen([mcp_bridge], env=base, stdin=subprocess.PIPE,
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
        if os.getenv("BUGSNITCH_BROWSER_SMOKE") == "1":
            run("node", ROOT / "web/tests/service-smoke.mjs", env=base)
            print("Headless Chrome signed in to the real service and inspected the persisted incident and devices.")

    checkout = tmp / "checkout"
    run("git", "init", "-q", str(checkout))
    run("git", "-C", str(checkout), "remote", "add", "origin", "https://github.com/example/fixture.git")
    homes = []
    initial = base
    if initial_server is not None:
        # The previous listener expects the workspace at the bundle root.
        bundle = Path(base.get("BUGSNITCH_WEB_DIR", ROOT / "web/dist"))
        workspace = bundle / "app/index.html"
        if workspace.is_file():
            legacy_bundle = tmp / "legacy-web"
            shutil.copytree(bundle, legacy_bundle)
            shutil.copyfile(workspace, legacy_bundle / "index.html")
            initial = {**base, "BUGSNITCH_WEB_DIR": str(legacy_bundle)}
    with server(initial, initial_server):
        with browser.open(origin + "/app/") as response:
            assert 'id="root"' in response.read().decode()
        api("login", {"token": base["BUGSNITCH_OWNER_TOKEN"]})
        project = api("projects", {"name": "Smoke project", "repository": "example/fixture"})["id"]
        for name in ("one", "two"):
            grant = api("pairings", {"name": name, "projectIds": [project]})
            local = {**base, "BUGSNITCH_HOME": str(tmp / name)}
            run(runner, "pair", "--server", origin, "--code", grant["code"], env=local)
            run(runner, "rotate", env=local)
            run(runner, "register", "--project", project, "--path", checkout, env=local)
            run(runner, "sync", env=local)
            homes.append(local)
        state = api("state")
        device_ids = {d["id"] for d in state["devices"]}
        assert len(device_ids) == 2
        print("Two independent runner processes paired, registered the fixture repository and synchronized.")
        exercise_interfaces(project)
        before = api("events?limit=150")
        assert before["events"] and not before["hasMore"]
    with server(base):
        # Use the original cookie, stored credentials, and event cursor on the
        # replacement listener. A release upgrade can occur between these calls.
        assert {d["id"] for d in api("state")["devices"]} == device_ids
        history = api("events?limit=150")
        assert history["events"][:len(before["events"])] == before["events"]
        for local in homes:
            run(runner, "sync", env=local)
        assert {d["id"] for d in api("state")["devices"]} == device_ids
        if initial_server is not None:
            exercise_interfaces(project)
            print("The upgraded listener accepted the original session, both registered runners and the chosen MCP bridge.")
        history = api("events?limit=150")
        assert history["events"] and not history["hasMore"]
        dump = tmp / "backup.dump"
        run("pg_dump", "-Fc", "--no-owner", "-f", dump, DB)
    run("pg_restore", "--exit-on-error", "--no-owner", "-d", RESTORE, dump)
    restored = {**base, "DATABASE_URL": f"postgresql:///{RESTORE}?sslmode=disable"}
    with server(restored):
        state = api("state")
        assert len(state["devices"]) == 2 and len(state["incidents"]) == 1 and state["projects"][0]["id"] == project
        assert api("events?limit=150") == history
        revoke = next(d["id"] for d in state["devices"] if d["name"] == "one")
        api(f"devices/{revoke}/revoke", {})
        tail = api("events?after=" + history["nextCursor"])
        assert any(e["kind"] == "device.revoked" and e["subjectId"] == revoke for e in tail["events"])
        proc = subprocess.run([runner, "sync"], env=homes[0], capture_output=True)
        assert proc.returncode and b"403" in proc.stderr
        run(runner, "sync", env=homes[1])
    print("Restart, migration replay, backup restore, session continuity, runner reconnect and revocation passed.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mcp-bridge", type=Path, default=BIN / "bugsnitch-mcp", help="Bugsnitch STDIO bridge executable, including an extracted plugin binary")
    parser.add_argument("--runner", type=Path, default=BIN / "bugsnitch", help="Runner executable to exercise, including a previous release")
    parser.add_argument("--initial-server", type=Path, help="Previous server executable with its matching siblings; restart and restore use the current package")
    args = parser.parse_args()
    if not os.getenv("PGHOST"):
        raise SystemExit("Set PGHOST, PGPORT and PGUSER for a disposable PostgreSQL instance with CREATEDB.")
    with contextlib.ExitStack() as cleanup:
        for database in (DB, RESTORE):
            run("createdb", database)
            cleanup.callback(run, "dropdb", database)
        with tempfile.TemporaryDirectory() as tmp:
            check(Path(tmp), args.mcp_bridge.resolve(), args.runner.resolve(),
                  args.initial_server.resolve() if args.initial_server else None)
