#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Compare packaged server idle commits and pickup through its fenced provider consumer."""
import argparse
import json
import os
from pathlib import Path
import secrets
import statistics
import struct
import subprocess
import sys
import tempfile
import time

from smoke import port, server


def frame(stream):
    header = stream.read(4)
    if not header:
        return b""
    length, = struct.unpack(">I", header)
    body = stream.read(length)
    if len(body) != length:
        raise EOFError("incomplete private frame")
    return header + body


def relay(real, counter, args):
    """Relay framed operations unchanged. Count only acknowledged job transactions."""
    if args != ["--coordinate"]:
        os.execv(real, [real, *args])
    with subprocess.Popen([real, "--coordinate"], stdin=subprocess.PIPE, stdout=subprocess.PIPE) as child:
        try:
            with open(counter, "ab", buffering=0) as count:
                while request := frame(sys.stdin.buffer):
                    child.stdin.write(request)
                    child.stdin.flush()
                    reply = frame(child.stdout)
                    if not reply:
                        break
                    if json.loads(request[4:])["op"].startswith("jobs.") and "value" in json.loads(reply[4:]):
                        count.write(b".")
                    sys.stdout.buffer.write(reply)
                    sys.stdout.buffer.flush()
        finally:
            child.stdin.close()


def observe_transactions(directory, binary):
    observer = directory / "count-jobs"
    counter = directory / "commits"
    counter.touch()
    script = Path(__file__).resolve()
    observer.write_text(
        f"#!{sys.executable}\nimport runpy,sys\nsys.path.insert(0,{str(script.parent)!r})\n"
        f"runpy.run_path({str(script)!r})['relay']({str(binary)!r},{str(counter)!r},sys.argv[1:])\n")
    observer.chmod(0o700)
    return observer, counter


def benchmark(binary):
    database = "bugsnitch_queue_bench_" + secrets.token_hex(8)
    # Never route fixture work to an operator's database service or provider.
    host = os.environ.get("PGHOST", "")
    if not (host.startswith("/") or host in ("localhost", "127.0.0.1", "::1")) or "," in host:
        raise ValueError("Set PGHOST to your disposable local PostgreSQL socket or loopback host")
    env = {k: v for k, v in os.environ.items() if not k.startswith(("PG", "BUGSNITCH_", "MCP_OAUTH_", "GITHUB_", "SENTRY_"))}
    for key in ("PGHOST", "PGPORT", "PGUSER", "PGPASSWORD"):
        if key in os.environ:
            env[key] = os.environ[key]

    def sql(statement, name=database):
        return subprocess.run(["psql", "-X", "-w", "-qAt", "-v", "ON_ERROR_STOP=1", "-d", name], env=env,
                              input=statement, text=True, check=True, capture_output=True).stdout.strip()

    def commits():
        return int(sql(f"SELECT xact_commit FROM pg_stat_database WHERE datname='{database}'", "postgres"))

    subprocess.run(["createdb", "-w", database], env=env, check=True)
    try:
        with tempfile.TemporaryDirectory() as tmp:
            # A failing private helper prevents all provider network I/O.
            observer, counter = observe_transactions(Path(tmp), binary / "bugsnitch-server-migrate")
            fixture = Path(tmp) / "provider-fixture"
            fixture.write_text('#!/bin/sh\ncase "$1" in --check-key) exit 0;; esac\nexit 1\n')
            fixture.chmod(0o700)
            config = {**env, "DATABASE_URL": f"postgresql:///{database}?sslmode=disable",
                      "BUGSNITCH_OWNER_TOKEN": secrets.token_hex(32), "BUGSNITCH_LISTEN": f"127.0.0.1:{port()}",
                      "GITHUB_APP_CLIENT_ID": "fixture", "GITHUB_APP_KEY_FILE": str(fixture),
                      "BUGSNITCH_PROVIDER": str(fixture), "BUGSNITCH_MIGRATOR": str(observer)}
            config["BUGSNITCH_ORIGIN"] = "http://" + config["BUGSNITCH_LISTEN"]
            with server(config, binary / "bugsnitch-server"):
                sql("""INSERT INTO projects(id,workspace_id,name,repository) VALUES('fixture','personal','Fixture','fixture/queue');
                       INSERT INTO github_mappings VALUES('personal','fixture',31,32,true)""")
                time.sleep(12)
                all_idle, job_idle = [], []
                for _ in range(3):
                    before, job_before = commits(), counter.stat().st_size
                    time.sleep(16)
                    all_idle.append(commits() - before)
                    job_idle.append(counter.stat().st_size - job_before)
                print("Idle acknowledged job transactions per 16 seconds:", job_idle, flush=True)
                print("All idle database commits including PostgreSQL maintenance:", all_idle, flush=True)
                elapsed = []
                for i in range(1, 9):
                    time.sleep([.07, .37, .57, .87][i % 4])
                    started = time.monotonic()
                    sql(f"""INSERT INTO planned_issue_state(workspace_id,project_id,number,github_id,installation_id,repository_id)
                            VALUES('personal','fixture',{i},{100+i},31,32)""")
                    while sql(f"SELECT failures FROM planned_issue_state WHERE number={i}") == "0":
                        if time.monotonic() - started > 12:
                            raise RuntimeError("Timed out waiting for the recorded provider failure")
                        time.sleep(.015)
                    elapsed.append(round((time.monotonic() - started) * 1000))
                print("Due drift insert through durable provider failure (ms):", elapsed, "median:", statistics.median(elapsed))
    finally:
        subprocess.run(["dropdb", "-w", "--force", database], env=env, check=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("bin", type=Path, help="Directory containing one release's matching server, domain and migrator")
    benchmark(parser.parse_args().bin.resolve())
