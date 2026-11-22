#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Exercise the real Rust listener with a disposable database and local signed provider fixtures."""
import base64
import contextlib
import datetime
import hashlib
import hmac
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import secrets
import ssl
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

from smoke import port, run, server


def certificates(directory):
    ca, leaf = directory / "ca.pem", directory / "server.pem"
    ca_key, key = directory / "ca.key", directory / "server.key"
    req = directory / "server.csr"
    config = directory / "cert.cnf"
    config.write_text("""[req]
distinguished_name=subject
[subject]
[ca]
basicConstraints=critical,CA:TRUE
keyUsage=critical,keyCertSign,cRLSign
[server]
basicConstraints=critical,CA:FALSE
subjectAltName=IP:127.0.0.1
extendedKeyUsage=serverAuth
keyUsage=digitalSignature,keyEncipherment
""")
    run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
        "-subj", "/CN=Bugsnitch test CA", "-config", config, "-extensions", "ca",
        "-keyout", ca_key, "-out", ca)
    run("openssl", "req", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=127.0.0.1",
        "-config", config, "-keyout", key, "-out", req)
    run("openssl", "x509", "-req", "-in", req, "-CA", ca, "-CAkey", ca_key, "-CAcreateserial",
        "-days", "2", "-extfile", config, "-extensions", "server", "-out", leaf)
    return ca, leaf, key


@contextlib.contextmanager
def authorization_server(directory, origin):
    ca, cert, key = certificates(directory)
    password = secrets.token_hex(16)
    auth = "Basic " + base64.b64encode(f"reader:{password}".encode()).decode()
    redirected = threading.Event()

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_GET(self):
            redirected.set()
            self.send_error(404)

        def do_POST(self):
            if self.path != "/introspect":
                redirected.set()
                self.send_error(404)
                return
            if self.headers.get("Authorization") != auth:
                self.send_error(401)
                return
            body = self.rfile.read(int(self.headers["Content-Length"]))
            form = urllib.parse.parse_qs(body.decode())
            token = form.get("token", [""])[0]
            assert form.get("token_type_hint") == ["access_token"]
            if token == "redirect":
                self.send_response(307)
                self.send_header("Location", issuer + "/unexpected")
                self.end_headers()
                return
            data = json.dumps({"active": token != "revoked", "iss": issuer, "sub": "owner",
                               "aud": ["wrong" if token == "wrong-resource" else origin + "/mcp"],
                               "exp": int(time.time()) + 60, "scope": "workspace.read incident.write" if token == "reporter" else "workspace.read"}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as httpd:
        issuer = f"https://127.0.0.1:{httpd.server_port}"
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(cert, key)
        httpd.socket = tls.wrap_socket(httpd.socket, server_side=True)
        thread = threading.Thread(target=httpd.serve_forever, daemon=True)
        thread.start()
        try:
            yield {"MCP_OAUTH_ISSUER": issuer, "MCP_OAUTH_INTROSPECTION": issuer + "/introspect",
                   "MCP_OAUTH_CLIENT_ID": "reader", "MCP_OAUTH_CLIENT_SECRET": password,
                   "MCP_OAUTH_OWNER_SUBJECT": "owner", "SSL_CERT_FILE": str(ca)}
            assert not redirected.is_set(), "introspection forwarded a credential on redirect"
        finally:
            httpd.shutdown()
            thread.join(timeout=2)


def exercise(db, directory):
    origin = f"http://127.0.0.1:{port()}"
    github_key, sentry_key = secrets.token_hex(32), secrets.token_hex(32)
    env = {**os.environ, "BUGSNITCH_LISTEN": origin.split("//")[1], "BUGSNITCH_ORIGIN": origin,
           "BUGSNITCH_OWNER_TOKEN": secrets.token_hex(32), "DATABASE_URL": f"postgresql:///{db}?sslmode=disable",
           "GITHUB_APP_CLIENT_ID": "", "GITHUB_APP_KEY_FILE": "", "GITHUB_WEBHOOK_SECRET": github_key,
           "SENTRY_CLIENT_SECRET": sentry_key, "BUGSNITCH_MCP_TOKEN": ""}
    def sql(statement):
        return run("psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1", "-d", db, "-c", statement).strip()

    def http(path, body=None, headers=None, want=200):
        req = urllib.request.Request(origin + path, data=body, headers=headers or {})
        try:
            response = urllib.request.urlopen(req, timeout=12)
        except urllib.error.HTTPError as err:
            response = err
        with response:
            data = response.read()
            assert response.status == want, (path, response.status, want, data[:180])
            return data

    def signed(provider, payload, headers, want, tamper=False):
        sentry = provider == "sentry"
        data = json.dumps(payload, indent=1).encode()
        signature = hmac.new((sentry_key if sentry else github_key).encode(), data, hashlib.sha256).hexdigest()
        headers = {**headers, "Content-Type": "application/json",
                   ("Sentry-Hook-Signature" if sentry else "X-Hub-Signature-256"): ("" if sentry else "sha256=") + signature}
        http("/hooks/" + provider, data + (b" " if tamper else b""), headers, want)

    def mcp(token, method, params, want=200):
        body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode()
        return json.loads(http("/mcp", body, {"Accept": "application/json, text/event-stream",
            "Content-Type": "application/json", "MCP-Protocol-Version": "2025-11-25", "Authorization": "Bearer " + token}, want))

    with authorization_server(directory, origin) as oauth, server({**env, **oauth}):
        uuid = "12ab425a-1111-2222-9999-aabbccddeeff"
        sql(f"""INSERT INTO projects(id,workspace_id,name,repository) VALUES ('p','personal','Fixture','example/app');
                INSERT INTO sentry_mappings(workspace_id,project_id,installation_uuid,provider_project)
                VALUES ('personal','p','{uuid}',91);
                INSERT INTO github_mappings(workspace_id,project_id,installation_id,repository_id)
                VALUES ('personal','p',777,888);""")
        metadata = json.loads(http("/.well-known/oauth-protected-resource/mcp"))
        assert metadata["authorization_servers"] == [oauth["MCP_OAUTH_ISSUER"]] and metadata["resource"] == origin + "/mcp"
        for token in ["wrong-resource", "redirect", "revoked"]:
            mcp(token, "tools/list", {}, 401)
        listed = mcp("valid", "tools/list", {})["result"]["tools"]
        assert {t["name"] for t in listed} == {"list_projects","list_decisions","list_attempts","get_project_status","inspect_attempt"}
        result = mcp("valid", "tools/call", {"name": "list_projects", "arguments": {}})["result"]
        assert json.loads(result["content"][0]["text"])[0]["id"] == "p"
        status = mcp("valid", "tools/call", {"name": "get_project_status", "arguments": {"projectId":"p"}})["result"]
        assert status["structuredContent"]["url"] == origin + "/#/overview?project=p"
        denied = mcp("valid", "tools/call", {"name": "report_incident", "arguments": {}})
        assert "error" in denied or denied["result"]["isError"]
        assert sql("SELECT count(*) FROM incidents") == "0"

        secret = "BUGSNITCH_SECRET_FIXTURE_DO_NOT_STORE"
        error = {"project": "91", "issue_id": 417, "event_id": "a"*32, "datetime": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                 "request": {"cookies": secret}, "message": secret, "tags": [["environment","staging"],["release","v3"]],
                 "exception":{"values":[{"type":"TypeError","value":secret,"stacktrace":{"frames":[
                    {"in_app":True,"lineno":9,"filename":"/src/total.js?token=" + secret}]}}]}}
        sentry = {"action":"created", "installation":{"uuid":uuid}, "data":{"error":error}, "unknown": "x"*70000}
        headers = {"Sentry-Hook-Resource":"error"}
        signed("sentry", sentry, headers, 401, tamper=True)
        error["project"] = "92"
        signed("sentry", sentry, headers, 403)
        error["project"] = "91"
        for _ in range(2):
            signed("sentry", sentry, headers, 202)
        rows = json.loads(sql("SELECT json_agg(i) FROM incident_inbox i"))
        assert len(rows)==1 and rows[0]["observed"]=="TypeError reported by Sentry" and secret not in json.dumps(rows)
        assert rows[0]["frames"]==[{"file":"total.js","line":9}] and rows[0]["environment"]=="staging"
        assert sql("SELECT count(*) FROM events WHERE kind='sentry.event_accepted'") == "1"

        signal={"action":"edited","issue":{"id":21,"number":2},"installation":{"id":777},
                "repository":{"id":888},"changes":{"title":{"from":secret}}}
        headers={"X-GitHub-Event":"issues","X-GitHub-Delivery":"11223344-1111-4000-8aaa-001122334455"}
        signed("github", signal, headers, 401, tamper=True)
        signed("github", {**signal, "installation":{"id":889}}, headers, 403)
        for _ in range(2):
            signed("github", signal, headers, 202)
        assert sql("SELECT count(*) FROM events WHERE kind='github.issue_change_received' AND actor_id='github/777'")=="1"
        headers.update({"X-GitHub-Event":"installation_repositories","X-GitHub-Delivery":"11223344-1111-4000-8aaa-001122334456"})
        signed("github",{"action":"removed","installation":{"id":777},"repositories_removed":[{"id":888}]},headers,202)
        assert sql("SELECT active FROM github_mappings WHERE repository_id=888")=="f"
        headers["X-GitHub-Event"]="issues"
        signed("github", signal, headers, 403)

        manual = {"projectId": "p", "requestId": "incident_fixture_001", "observed": "Preview failed\nAuthorization: Bearer " + secret,
                  "expected": "Preview opens", "steps": "Open preview"}
        created = mcp("reporter", "tools/call", {"name": "report_incident", "arguments": manual})
        assert created["result"]["structuredContent"]["id"]
        projected = sql("SELECT observed FROM incidents WHERE source='manual'")
        assert projected == "Preview failed\n[redacted]" and secret not in projected
        assert sql("SELECT count(*) FROM events WHERE kind='incident.observed' AND actor_id='oauth-owner-grant'") == "1"


if __name__ == "__main__":
    if not os.getenv("PGHOST"):
        raise SystemExit("Set PGHOST, PGPORT and PGUSER for a disposable PostgreSQL instance with CREATEDB.")
    database = "bugsnitch_inbound_" + secrets.token_hex(6)
    run("createdb",database)
    try:
        with tempfile.TemporaryDirectory() as directory:
            exercise(database,Path(directory))
    finally:
        run("dropdb",database)
    print("Signed GitHub and Sentry, redaction, duplicate delivery, revoked mapping, TLS token introspection and scoped MCP passed.")
