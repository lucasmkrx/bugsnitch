# SPDX-License-Identifier: MPL-2.0
"""Local PostgreSQL black-box tests, launched by the Cargo integration test."""
import hashlib
import ipaddress
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time
from contextlib import contextmanager
from urllib.parse import parse_qsl, unquote, urlencode, urlsplit, urlunsplit

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

    def launch(self, schema, timeout=12000, url=None):
        # Exercise the same search_path extension as the Go service; do not pass
        # PGOPTIONS to the executable or let it hide broken URL handling.
        query = urlencode({**self.params, "search_path": schema, "application_name": self.tag + "_worker"})
        dsn = url or urlunsplit(self.url._replace(query=query))
        env = {k: v for k, v in self.env.items() if not k.startswith("PG")}
        env.update(DATABASE_URL=dsn, BUGSNITCH_MIGRATION_TIMEOUT_MS=str(timeout))
        return subprocess.Popen([self.binary], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

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
    migrations = sorted(SOURCE.glob("*.sql"))
    assert migrations and migrations[0].name == "001_core.sql"
    with db.schema("fresh") as schema:
        concurrent_start(db, schema)
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
        db.finish(db.launch(schema), success=True)
        verify_ledger(db, schema, migrations)
        assert db.sql("SELECT applied_at='2001-01-01 UTC'::timestamptz FROM bugsnitch_migrations WHERE name='001_core.sql'", schema) == "t"
    invalid = f"postgresql://fixture:{MARKER}@127.0.0.1:invalidport/local"
    db.finish(db.launch("unused", url=invalid), success=False)
    print("Verified fresh and legacy ledgers, concurrent startups, lock timeout, checksums, rollback and redacted errors.")


if __name__ == "__main__":
    main(sys.argv[1])
