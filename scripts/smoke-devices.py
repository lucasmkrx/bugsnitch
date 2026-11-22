#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Verify the packaged Rust device HTTP routes against a disposable local PostgreSQL schema."""
import hashlib
from http.cookiejar import CookieJar
import json
import os
from pathlib import Path
import secrets
import sys
from urllib.error import HTTPError
from urllib.parse import urlencode, urlunsplit
from urllib.request import build_opener, HTTPCookieProcessor, Request

from smoke import port, server

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "rust/server-migrate/tests"))
from database import Fixture


def check(db, schema):
    owner_secret, token, replacement = (secrets.token_hex(32) for _ in range(3))
    address = f"127.0.0.1:{port()}"
    origin = "http://" + address
    env = {k: os.environ[k] for k in ("HOME", "PATH", "SSL_CERT_FILE", "SSL_CERT_DIR") if k in os.environ}
    env.update(BUGSNITCH_LISTEN=address, BUGSNITCH_ORIGIN=origin, BUGSNITCH_OWNER_TOKEN=owner_secret,
               DATABASE_URL=urlunsplit(db.url._replace(query=urlencode({**db.params, "search_path": schema}))))
    browser = build_opener(HTTPCookieProcessor(CookieJar()))
    machine = build_opener()
    marker = "SYNTHETIC_DEVICE_SECRET"

    def call(path, body=None, *, owner=False, credential=None, status=200):
        headers = {"Origin": origin, "Content-Type": "application/json"}
        if credential is not None:
            headers["Authorization"] = "Bearer " + credential
        request = Request(origin + "/api/v1/" + path, headers=headers,
                          data=json.dumps(body).encode() if body is not None else None)
        try:
            response = (browser if owner else machine).open(request, timeout=10)
        except HTTPError as exc:
            response = exc
        with response:
            raw = response.read()
            assert response.status == status, f"{path}: got HTTP {response.status}, expected {status}"
            assert response.headers.get("X-Request-ID"), path
            assert not any(secret.encode() in raw for secret in (owner_secret, token, replacement, marker)), path
            return json.loads(raw)

    with server(env):
        call("login", {"token": owner_secret}, owner=True)
        project = call("projects", {"name": "Native routes", "repository": "example/native-routes"}, owner=True, status=201)["id"]
        grant = call("pairings", {"name": "Fixture", "projectIds": [project]}, owner=True, status=201)
        enrollment = {"protocol": 1, "code": grant["code"], "credential": token, "platform": "linux/amd64"}
        call("device/pair", {**enrollment, "protocol": 0}, status=426)
        device = call("device/pair", enrollment)["deviceId"]
        assert call("device/pair", enrollment)["deviceId"] == device
        call("device/pair", {**enrollment, "credential": replacement}, status=403)
        call("device/sync", {"protocol": 1}, status=403)
        call("device/sync", {"protocol": 2}, credential=token, status=426)
        assert call("device/next", {"protocol": 1, "projectId": project}, credential=token)["reason"]
        permit = {"protocol": 1, "workspaceId": "personal", "deviceId": device, "permitId": secrets.token_hex(16),
                  "role": "manager", "projectId": project, "version": 1}
        assert call("device/model-permit", permit, credential=token)["version"] == 1
        call("device/model-permit", {**permit, "version": 2}, credential=token)
        call("device/model-permit", {**permit, "role": "other"}, credential=token, status=400)
        subject = {"attemptId": "native_device_smoke", "issueNumber": 7, "projectId": project, "baseSha": "a" * 40}
        admit = {"protocol": 1, **subject, "operation": "admit", "sequence": 1}
        receipt = call("device/admission", admit, credential=token)
        assert receipt == call("device/admission", admit, credential=token)
        call("device/admission", {**admit, "arbitrary": marker}, credential=token, status=400)
        report = {"id": subject["attemptId"], "projectId": project, "issueNumber": 7,
                  "baseSha": subject["baseSha"], "candidateSha": "", "phase": "implement", "state": "running", "version": 1}
        call("device/sync", {"protocol": 1, "attempts": [{**report, "evidence": [{"phase": "plan", "raw": marker}]}]},
             credential=token, status=400)
        assert call("device/sync", {"protocol": 1, "attempts": [report]}, credential=token)["stageTimings"]
        lease = {"protocol": 1, **subject, "workspaceId": "personal", "deviceId": device,
                 "subjectVersion": 1, "sequence": 1, "modelTurn": False}
        assert call("device/worker-lease", lease, credential=token)["validSeconds"] > 0
        control_path = "attempts/" + subject["attemptId"] + "/control"
        control = {"operation": "pause", "requestId": "native_control", "version": 1}
        cmd = call(control_path, control, owner=True, status=202)
        assert call(control_path, control, owner=True, status=202)["id"] == cmd["id"]
        assert call("device/worker-lease", lease, credential=token)["control"]["id"] == cmd["id"]
        ack = {"id": cmd["id"], "status": "completed"}
        report.update(state="completed", phase="wrap_up", candidateSha="b" * 40, version=2)
        call("device/sync", {"protocol": 1, "acks": [ack], "attempts": [report]}, credential=token)
        call("device/admission", {**admit, "sequence": 2, "operation": "finish", "state": "completed"}, credential=token)
        diff_path = "attempts/" + subject["attemptId"] + "/diff"
        diff = {"protocol": 1, "attemptId": subject["attemptId"], "version": 2,
                "baseSha": subject["baseSha"], "candidateSha": report["candidateSha"]}

        def share(text, expected, **changes):
            digest = hashlib.sha256(text.encode()).hexdigest()
            call("device/share-diff", {**diff, "text": text, "digest": digest, **changes}, credential=token, status=expected)

        share("Authorization: " + marker, 400)
        share("+ return total", 400, digest="0" * 64)
        share("+ return total", 403, version=1)
        share("+ return total", 200)
        share("+ return total", 200)
        share("+ return different", 409)
        call(diff_path, status=401)
        assert call(diff_path, owner=True)["text"] == "+ return total"
        call("workspace/dispatch", {"stopped": True, "version": 1, "requestId": "native_emergency"}, owner=True)
        history = call("events?limit=150", owner=True)
        assert sum(e["kind"] == "artifact.diff_shared" and e["actorId"] == device for e in history["events"]) == 1
        assert max(int(e["cursor"]) for e in history["events"]) == int(history["nextCursor"])
        call("events?after=0&limit=151", owner=True, status=400)
        call("device/rotate", {"protocol": 1, "credential": replacement}, credential=token)
        call("device/rotate", {"protocol": 1, "credential": replacement}, credential=token)
    with server(env):
        call("device/sync", {"protocol": 1}, credential=token, status=403)
        call("device/sync", {"protocol": 1}, credential=replacement)
        call("devices/" + device + "/revoke", {}, owner=True)
        call("device/sync", {"protocol": 1}, credential=replacement, status=403)
        assert [e["kind"] for e in call("events?after=" + history["nextCursor"], owner=True)["events"]] == [
            "device.rotated", "device.revoked"]
    print("Native device routes passed over HTTP with PostgreSQL, including replay, authority, redaction and restart.")


if __name__ == "__main__":
    database = Fixture(str(ROOT / "bin/bugsnitch-server-migrate"))
    with database.schema("native_http") as schema:
        check(database, schema)
