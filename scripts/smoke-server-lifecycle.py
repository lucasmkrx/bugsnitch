#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Exercise the matched binary dependency and the Rust server/Go worker lifeline on Unix."""
import os
from pathlib import Path
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from urllib.request import urlopen

from smoke import BIN, port

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "rust/server-migrate/tests"))
from database import Fixture


def wait_for(check, description):
    until = time.monotonic() + 12
    while time.monotonic() < until:
        if check():
            return
        time.sleep(0.05)
    raise RuntimeError(description)


def run(schema, db):
    with tempfile.TemporaryDirectory() as tmp:
        address = f"127.0.0.1:{port()}"
        origin = "http://" + address
        key = secrets.token_hex(32)
        env = {"TMPDIR": tmp, "BUGSNITCH_ORIGIN": origin, "BUGSNITCH_LISTEN": address,
               "BUGSNITCH_OWNER_TOKEN": key, "PATH": os.environ["PATH"],
               "DATABASE_URL": db.dsn(schema)}
        # Copy the executable so the OS resolves siblings from this empty directory.
        isolated = Path(tmp) / "bugsnitch-server"
        shutil.copy2(BIN / isolated.name, isolated)
        missing = subprocess.run([isolated], env=env, capture_output=True, timeout=8)
        assert missing.returncode and b"bugsnitch-domain" in missing.stderr and key.encode() not in missing.stderr
        for kill_parent in (False, True):
            with subprocess.Popen([BIN / isolated.name], env=env, stdout=subprocess.DEVNULL,
                                  stderr=subprocess.DEVNULL) as parent:
                try:
                    def ready():
                        if parent.poll() is not None:
                            raise RuntimeError("Server failed before readiness")
                        try:
                            with urlopen(origin + "/readyz", timeout=0.5) as reply:
                                return reply.status == 200
                        except OSError:
                            return False
                    wait_for(ready, "Server readiness timed out")
                    # The Go worker owns this private socket and unlinks it on ordinary shutdown.
                    sockets = list(Path(tmp).glob("bugsnitch-*/domain.sock"))
                    assert len(sockets) == 1 and sockets[0].parent.stat().st_mode & 0o077 == 0
                    assert sockets[0].stat().st_mode & 0o777 == 0o600
                    if kill_parent:
                        parent.kill()
                        parent.wait(timeout=5)
                        wait_for(lambda: not sockets[0].exists(), "Orphaned domain worker retained its socket")
                    else:
                        pid = subprocess.check_output(["pgrep", "-P", str(parent.pid), "-f", "bugsnitch-domain"]).split()
                        assert len(pid) == 1
                        os.kill(int(pid[0]), signal.SIGKILL)
                        assert parent.wait(timeout=5) != 0
                finally:
                    if parent.poll() is None:
                        parent.kill()
        print("A missing worker prevented startup, worker loss stopped the listener, and parent loss stopped the worker.")


if __name__ == "__main__":
    db = Fixture(BIN / "bugsnitch-server-migrate")
    with db.schema("lifecycle") as schema:
        run(schema, db)
