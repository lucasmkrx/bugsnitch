#!/usr/bin/env python3
"""Build a GitHub check-run request from a trusted deployed-behavior probe."""
import argparse
from datetime import datetime, timezone
import json
import os
import re
import subprocess
import sys
import tempfile
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None


def identity(url):
    parsed = urlsplit(url)
    loopback = parsed.scheme == "http" and parsed.hostname in {"127.0.0.1", "::1"}
    if (parsed.scheme != "https" and not loopback) or not parsed.hostname or parsed.username or parsed.password or parsed.fragment:
        raise ValueError("supply the HTTPS serving manifest URL")
    req = Request(url, headers={"Cache-Control": "no-cache", "Accept": "application/json"})
    with build_opener(NoRedirect()).open(req, timeout=15) as stream:
        raw = stream.read(4097)
    if len(raw) > 4096:
        raise ValueError("serving manifest too large")
    doc = json.loads(raw)
    return {k: doc[k] for k in ("build", "candidate", "deployment")}


def stamp():
    return datetime.now(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--monitor", required=True)
    parser.add_argument("--name", required=True)
    parser.add_argument("--manifest", required=True)
    parser.add_argument("--candidate", required=True)
    parser.add_argument("--build", required=True)
    parser.add_argument("--deployment", type=int, required=True)
    parser.add_argument("--status", type=int, required=True)
    parser.add_argument("--timeout", type=int, default=600)
    parser.add_argument("probe", nargs=argparse.REMAINDER)
    a = parser.parse_args()
    command = a.probe[1:] if a.probe[:1] == ["--"] else a.probe
    if not command or not re.fullmatch("[0-9a-f]{32}", a.monitor) or not re.fullmatch("[0-9a-f]{40}|[0-9a-f]{64}", a.candidate):
        parser.error("a monitor, full candidate revision and probe command are required")
    if not re.fullmatch("[0-9a-f]{64}", a.build) or min(a.status, a.deployment, a.timeout) <= 0:
        parser.error("a build SHA-256, deployment ID, successful status ID and positive timeout are required")
    subject = {"build": a.build, "candidate": a.candidate, "deployment": a.deployment}
    started = stamp()
    if identity(a.manifest) != subject:
        raise ValueError("serving manifest differs from the requested deployment")
    env = {k: os.environ[k] for k in ("PATH", "LANG", "LC_ALL", "TMPDIR") if k in os.environ}
    with tempfile.TemporaryFile() as output:
        result = subprocess.run(command, env=env, timeout=a.timeout, stdout=output, check=False)
        output.seek(0)
        raw = output.read(4097)
    if result.returncode not in (0, 1) or len(raw) > 4096:
        raise ValueError("probe failed to produce a bounded observation")
    if identity(a.manifest) != subject:
        raise ValueError("the served deployment changed during the observation")
    observation = json.loads(raw)
    if not isinstance(observation, dict) or set(observation) - {"coverage", "exposures"}:
        raise ValueError("probe output must be a JSON object with optional coverage and exposures")
    if "coverage" in observation and type(observation["coverage"]) is not bool:
        raise ValueError("coverage must be a boolean")
    if "exposures" in observation and (type(observation["exposures"]) is not int or not 0 <= observation["exposures"] < 2**63):
        raise ValueError("exposures must be a nonnegative integer")
    correlation = {"monitor": a.monitor, "deployment": a.deployment, "status": a.status, "build": a.build, **observation}
    print(json.dumps({"name": a.name, "head_sha": a.candidate, "status": "completed",
                      "started_at": started, "completed_at": stamp(),
                      "conclusion": "success" if result.returncode == 0 else "failure",
                      "external_id": "bugsnitch/v1 " + json.dumps(correlation, separators=(",", ":"))}))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, TypeError, subprocess.TimeoutExpired) as error:
        print(f"Deployment probe unavailable: {error}", file=sys.stderr)
        sys.exit(2)
