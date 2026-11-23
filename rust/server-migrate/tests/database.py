# SPDX-License-Identifier: MPL-2.0
"""Local PostgreSQL black-box tests, launched by the Cargo integration test."""
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import secrets
import struct
import subprocess
import sys
import time
from contextlib import contextmanager
from unittest.mock import patch
from urllib.parse import parse_qsl, unquote, urlencode, urlsplit

LOCK = 628719844
SOURCE = Path(__file__).resolve().parents[3] / "internal/storage/migrations"
MARKER = "TEST_ONLY_RUST_MIGRATION_SECRET"


class Fixture:
    def __init__(self, binary):
        self.binary = binary
        raw = os.environ.get("BUGSNITCH_TEST_DATABASE_URL") or os.environ.get("BUGSNITCH_TEST_DSN")
        if not raw:
            raise ValueError("Set BUGSNITCH_TEST_DATABASE_URL to a disposable local PostgreSQL URL")
        self.url = urlsplit(raw)
        if self.url.scheme not in {"postgres", "postgresql"} or self.url.fragment:
            raise ValueError("Use a postgres:// or postgresql:// URL")
        pairs = parse_qsl(self.url.query, keep_blank_values=True, strict_parsing=True)
        self.params = dict(pairs)
        allowed = {"host", "port", "user", "password", "search_path", "sslmode", "connect_timeout", "application_name"}
        if len(self.params) != len(pairs) or self.params.keys() - allowed:
            raise ValueError("Use a simple local test URL without duplicate or alternate routing options")
        authority = unquote(self.url.hostname or "")
        host = self.params.get("host", authority)
        for address in (authority, host):
            if address and not address.startswith("/") and address != "localhost":
                if "," in address or not ipaddress.ip_address(address).is_loopback:
                    raise ValueError("The integration test only connects to a local socket or loopback address")
        if not host or "," in host or not self.url.path.strip("/"):
            raise ValueError("An explicit local host and database are required")
        self.params.pop("search_path", None)
        # psql receives secrets via its environment. Never place a connection URL in argv.
        self.env = {k: v for k, v in os.environ.items() if not k.startswith("PG")}
        self.env.update(PGHOST=host, PGDATABASE=unquote(self.url.path[1:]), PGCONNECT_TIMEOUT="4")
        settings = {
            "PGPORT": self.params.get("port", self.url.port),
            "PGUSER": self.params.get("user", unquote(self.url.username or "")),
            "PGPASSWORD": self.params.get("password", unquote(self.url.password or "")),
            "PGSSLMODE": self.params.get("sslmode"),
        }
        self.env.update({k: str(v) for k, v in settings.items() if v})
        self.tag = "bugsnitch_mig_" + secrets.token_hex(8)
        self.env["PGAPPNAME"] = self.tag
        self.psql = ["psql", "-X", "-w", "-qAt", "-v", "ON_ERROR_STOP=1"]

    def sql(self, statement, schema=None):
        env = self.env.copy()
        if schema:
            env["PGOPTIONS"] = "-c search_path=" + schema
        result = subprocess.run(self.psql, input=statement, capture_output=True, text=True,
                                timeout=12, env=env)
        if result.returncode:
            raise AssertionError(f"PostgreSQL fixture setup or assertion failed (psql {result.returncode})")
        return result.stdout.strip()

    @contextmanager
    def schema(self, suffix):
        name = self.tag + "_" + suffix
        self.sql("CREATE SCHEMA " + name)
        try:
            yield name
        finally:
            self.sql("DROP SCHEMA " + name + " CASCADE")

    def dsn(self, schema, **overrides):
        query = urlencode({**self.params, "search_path": schema, **overrides})
        # A PostgreSQL URI keeps the // even when the host is supplied in the query.
        return f"{self.url.scheme}://{self.url.netloc}{self.url.path}?{query}"

    def launch(self, schema, timeout=12000, url=None, *, coordinate=False):
        # Exercise the same search_path extension as the Go service; do not pass
        # PGOPTIONS to the executable or let it hide broken URL handling.
        dsn = url or self.dsn(schema, application_name=self.tag + "_worker")
        env = {k: v for k, v in self.env.items() if not k.startswith("PG")}
        env.update(DATABASE_URL=dsn, BUGSNITCH_MIGRATION_TIMEOUT_MS=str(timeout))
        args = [self.binary, "--coordinate"] if coordinate else [self.binary]
        return subprocess.Popen(args, env=env, stdin=subprocess.PIPE if coordinate else None,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    @staticmethod
    def finish(process, success, timeout=16):
        try:
            stdout, stderr = process.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            process.kill()
            process.communicate()
            raise AssertionError("Migrator exceeded the fixture deadline") from None
        output = stdout + stderr
        assert MARKER.encode() not in output, "Migrator printed secret-bearing input"
        assert (process.returncode == 0) == success, f"Unexpected migrator exit {process.returncode}"
        return output


def verify_coordination_frames(db, schema):
    ready = {"version": 1, "op": "system.ready", "input": None, "actor": ["service", "test"]}
    requests = [json.dumps({**ready, "version": 2}).encode(), b"{", json.dumps({**ready, "op": MARKER}).encode(),
                json.dumps(ready).encode()]
    frames = b"".join(struct.pack(">I", len(body)) + body for body in requests)
    child = db.launch(schema, coordinate=True)
    response, errors = child.communicate(frames, timeout=8)
    assert child.returncode == 0 and MARKER.encode() not in response + errors
    replies = []
    while response:
        length, = struct.unpack(">I", response[:4])
        assert 0 < length <= len(response) - 4
        replies.append(json.loads(response[4:4 + length]))
        response = response[4 + length:]
    assert len(replies) == len(requests) and all(r["version"] == 1 for r in replies)
    assert [r["error"]["status"] for r in replies[:-1]] == [400, 400, 400]
    assert replies[-1]["value"] == 1
    for incomplete in (struct.pack(">I", 4 * 1024 * 1024 + 1), struct.pack(">I", 100) + b"{"):
        child = db.launch(schema, coordinate=True)
        response, errors = child.communicate(incomplete, timeout=8)
        assert child.returncode != 0 and not response and not errors


def verify_ledger(db, schema, migrations):
    records = db.sql("SELECT name,digest FROM bugsnitch_migrations ORDER BY name", schema).splitlines()
    assert records == [p.name + "|" + hashlib.sha256(p.read_bytes()).hexdigest() for p in migrations], (
        "Ledger filenames or SHA-256 values differ from the checked-in Go migration sources"
    )
    assert db.sql("SELECT account_id FROM workspaces WHERE id='personal'", schema) == "owner"
    assert db.sql("""SELECT count(*) FROM information_schema.columns
        WHERE table_schema=current_schema() AND table_name='devices'
        AND column_name='diagnostics_since'""", schema) == "1"


def wait_for(poll, description):
    deadline = time.monotonic() + 6
    while time.monotonic() < deadline:
        if poll():
            return
        time.sleep(0.04)
    raise AssertionError(description)


def concurrent_start(db, schema):
    env = {**db.env, "PGAPPNAME": db.tag + "_holder"}
    with subprocess.Popen(db.psql, env=env, text=True, stdin=subprocess.PIPE,
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL) as holder:
        children = []
        try:
            holder.stdin.write(f"BEGIN; SELECT pg_advisory_xact_lock({LOCK});\n")
            holder.stdin.flush()
            def locks(suffix, granted):
                return int(db.sql(f"""SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid
                    WHERE a.datname=current_database() AND a.application_name='{db.tag}_{suffix}'
                      AND l.locktype='advisory' AND l.objid={LOCK} AND l.classid=0
                      AND l.objsubid=1 AND l.granted IS {granted}"""))
            wait_for(lambda: locks("holder", "TRUE") == 1, "Fixture could not hold the migration lock")
            db.finish(db.launch(schema, timeout=700), success=False, timeout=8)
            for _ in range(2):
                children.append(db.launch(schema))
            wait_for(lambda: locks("worker", "FALSE") == 2, "Both migrators must wait for the shared lock")
            holder.stdin.write("COMMIT;\n\\q\n")
            holder.stdin.flush()
            holder.wait(timeout=3)
            for process in children:
                db.finish(process, success=True)
        finally:
            if holder.poll() is None:
                holder.stdin.close()
                holder.wait(timeout=3)
            for process in children:
                if process.poll() is None:
                    process.kill()
                    process.communicate()


def main(binary):
    db = Fixture(binary)
    with patch.dict(os.environ, BUGSNITCH_TEST_DATABASE_URL="postgresql:///fixture?host=%2Ftmp"):
        assert Fixture(binary).dsn("example") == "postgresql:///fixture?host=%2Ftmp&search_path=example"
    migrations = sorted(SOURCE.glob("*.sql"))
    assert migrations and migrations[0].name == "001_core.sql"
    with db.schema("fresh") as schema:
        concurrent_start(db, schema)
        verify_coordination_frames(db, schema)
        verify_ledger(db, schema, migrations)
        ledger = "SELECT name,digest,applied_at FROM bugsnitch_migrations ORDER BY name"
        before = db.sql(ledger, schema)
        db.finish(db.launch(schema), success=True)
        assert db.sql(ledger, schema) == before, "Rerun rewrote migration history"
        db.sql("UPDATE bugsnitch_migrations SET digest='tampered' WHERE name='001_core.sql'", schema)
        db.finish(db.launch(schema), success=False)
        assert db.sql("SELECT digest FROM bugsnitch_migrations WHERE name='001_core.sql'", schema) == "tampered"
    with db.schema("rollback") as schema:
        # This known table collides in migration 002, after 001 has run.
        db.sql("CREATE TABLE sentry_mappings (guard integer); INSERT INTO sentry_mappings VALUES (72)", schema)
        db.finish(db.launch(schema), success=False)
        assert db.sql("SELECT to_regclass('accounts') IS NULL AND to_regclass('bugsnitch_migrations') IS NULL", schema) == "t"
        assert db.sql("SELECT guard FROM sentry_mappings", schema) == "72"
    with db.schema("legacy") as schema:
        first = migrations[0]
        digest = hashlib.sha256(first.read_bytes()).hexdigest()
        db.sql("BEGIN; CREATE TABLE bugsnitch_migrations (name text PRIMARY KEY,digest text NOT NULL,"
               "applied_at timestamptz NOT NULL DEFAULT now()); " + first.read_text() +
               f"INSERT INTO bugsnitch_migrations VALUES ('001_core.sql','{digest}','2001-01-01 UTC'); COMMIT;", schema)
        db.sql("""INSERT INTO workspaces(id, account_id, name) VALUES ('other','owner','Other');
               INSERT INTO events(workspace_id,kind,subject_id)
               VALUES ('personal','legacy','first'),('other','legacy','private'),('personal','legacy','second')""", schema)
        db.finish(db.launch(schema), success=True)
        verify_ledger(db, schema, migrations)
        assert db.sql("SELECT workspace_id,subject_id,position FROM events ORDER BY seq", schema).splitlines() == [
            "personal|first|1", "other|private|1", "personal|second|2"
        ]
        db.sql("INSERT INTO events(workspace_id,kind,subject_id) VALUES ('personal','new','third')", schema)
        assert db.sql("SELECT position FROM events WHERE kind='new'", schema) == "3"
        assert db.sql("SELECT applied_at='2001-01-01 UTC'::timestamptz FROM bugsnitch_migrations WHERE name='001_core.sql'", schema) == "t"
    with db.schema("deployments") as schema:
        # Build the last schema that accepted validations without a deployment ID.
        db.sql("CREATE TABLE bugsnitch_migrations (name text PRIMARY KEY, digest text NOT NULL,"
               "applied_at timestamptz NOT NULL DEFAULT now())", schema)
        for migration in migrations:
            if migration.name >= "022_validation_deployment.sql":
                break
            digest = hashlib.sha256(migration.read_bytes()).hexdigest()
            db.sql("BEGIN; " + migration.read_text() +
                   f"INSERT INTO bugsnitch_migrations(name,digest) VALUES('{migration.name}','{digest}'); COMMIT;", schema)
        db.sql("""INSERT INTO projects(id, workspace_id, name, repository) VALUES ('p','personal','Fixture','fixture/checkout');
            INSERT INTO incidents(id,workspace_id,project_id,source,group_key,observed,expected,steps,first_seen,last_seen,occurrences)
            VALUES ('incident','personal','p','manual','checkout','shipping total','','',now(),now(),1);
            INSERT INTO incident_evidence(workspace_id,project_id,incident_id,command_key,payload_hash,kind,candidate,
                environment,build,evidence,effective_at,received_at,positive)
            SELECT 'personal','p','incident',key,'hash',kind,'revision','fixture','build','retained evidence',
                '2001-01-01 UTC'::timestamptz + effective * interval '1 second',
                '2001-01-01 UTC'::timestamptz + received * interval '1 second',kind='validation'
            FROM (VALUES ('deploy-1','deployment',1,2), ('before','validation',0,0),
                ('tie','validation',1,2), ('first','validation',3,3),
                ('deploy-2','deployment',4,4), ('ambiguous','validation',5,5)) v(key,kind,effective,received)""", schema)
        db.finish(db.launch(schema), success=True)
        assert db.sql("""SELECT command_key,coalesce(deployment_key,'') FROM incident_evidence
                      WHERE kind='validation' ORDER BY command_key""", schema).splitlines() == [
            "ambiguous|", "before|", "first|deploy-1", "tie|"
        ]
    invalid = f"postgresql://fixture:{MARKER}@127.0.0.1:invalidport/local"
    db.finish(db.launch("unused", url=invalid), success=False)
    print("Verified fresh and legacy ledgers, concurrent startups, lock timeout, checksums, rollback, versioned coordination frames and redacted errors.")


if __name__ == "__main__":
    main(sys.argv[1])
