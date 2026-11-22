#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Measure packaged MCP reads on loopback against a new disposable PostgreSQL database."""
import argparse
import http.client
import json
import os
from pathlib import Path
import secrets
import statistics
import time

from smoke import port, run, server


def measure(binaries):
    database = "bugsnitch_bench_" + secrets.token_hex(8)
    address = f"127.0.0.1:{port()}"
    token = secrets.token_hex(32)
    # Each release uses its own matching sibling binaries.
    host = os.getenv("PGHOST", "")
    if "," in host or not (host.startswith("/") or host in {"localhost", "127.0.0.1", "::1"}):
        raise ValueError("Use a disposable PostgreSQL instance on a local socket or loopback host")
    env = {k: v for k, v in os.environ.items()
           if not k.startswith(("PG", "BUGSNITCH_", "MCP_OAUTH_", "GITHUB_", "SENTRY_"))}
    for key in ("PGHOST", "PGPORT", "PGUSER", "PGPASSWORD"):
        if key in os.environ:
            env[key] = os.environ[key]
    env.update(BUGSNITCH_LISTEN=address, BUGSNITCH_ORIGIN="http://" + address,
               BUGSNITCH_OWNER_TOKEN=secrets.token_hex(32), BUGSNITCH_MCP_TOKEN=token,
               BUGSNITCH_MCP_SCOPES="workspace.read", DATABASE_URL=f"postgresql:///{database}?sslmode=disable")
    run("createdb", "-w", database, env=env)
    try:
        with server(env, binaries / "bugsnitch-server"):
            run("psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", database, "-c", """
                INSERT INTO projects(id,workspace_id,name,repository) VALUES ('p','personal','Fixture','example/profile');
                INSERT INTO devices(id,workspace_id,name,platform) VALUES ('d','personal','Fixture machine','darwin');
                INSERT INTO device_projects VALUES ('personal','d','p');
                INSERT INTO attempt_reports(id,workspace_id,project_id,device_id,issue_number,base_sha,
                    candidate_sha,phase,state,version,evidence)
                SELECT 'a' || i,'personal','p','d',i,repeat('1',40),repeat('2',40),'wrap_up','completed',8,
                    (SELECT jsonb_agg(jsonb_build_object('phase',stage,'candidateSha',repeat('2',40),
                        'changedFiles',3,'exitCode',0,'durationMs',250))
                     FROM unnest(ARRAY['plan','implement','validate','review','wrap_up']) stage)
                FROM generate_series(1,100) i""", env=env)
            connection = http.client.HTTPConnection(address, timeout=10)
            try:
                for name, arguments in [("get_project_status", {"projectId": "p"}), ("list_attempts", {})]:
                    samples = []
                    for i in range(36):
                        body = json.dumps({"jsonrpc": "2.0", "id": i, "method": "tools/call",
                                           "params": {"name": name, "arguments": arguments}})
                        start = time.perf_counter_ns()
                        connection.request("POST", "/mcp", body, {"Content-Type": "application/json",
                            "Accept": "application/json, text/event-stream", "Authorization": "Bearer " + token,
                            "MCP-Protocol-Version": "2025-11-25"})
                        response = connection.getresponse()
                        data = response.read()
                        elapsed = (time.perf_counter_ns() - start) / 1e6
                        assert response.status == 200
                        result = json.loads(data)["result"]
                        assert not result.get("isError")
                        if name == "list_attempts":
                            assert len(json.loads(result["content"][0]["text"])) == 100
                        else:
                            assert result["structuredContent"]["lastReportedRunning"] == 0
                        if i >= 5:
                            samples.append(elapsed)
                    print(f"{binaries.name} {name}: median {statistics.median(samples):.3f} ms "
                          f"(31 warm requests, {len(data)} response bytes)", flush=True)
            finally:
                connection.close()
    finally:
        run("dropdb", "-w", database, env=env)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("release", type=Path, nargs="+", help="Directory containing matching hosted binaries")
    args = parser.parse_args()
    if not os.getenv("PGHOST"):
        parser.error("Set PGHOST, PGPORT and PGUSER for a disposable PostgreSQL instance with CREATEDB")
    for release in args.release:
        measure(release.resolve())
