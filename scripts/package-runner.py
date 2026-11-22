#!/usr/bin/env python3
"""Build a matching macOS runner and journal executable and package them together."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile

from go_notices import files as go_notices
from rust_notices import files as rust_notices

ROOT = Path(__file__).resolve().parents[1]


def package_notices(archive, target, arch):
    for path, name in rust_notices("bugsnitch-runner-journal", target):
        archive.add(path, arcname="licenses/" + name)
    env = {**os.environ, "CGO_ENABLED": "0", "GOOS": "darwin", "GOARCH": arch}
    for path, name in go_notices("./cmd/bugsnitch", env):
        archive.add(path, arcname="licenses/" + name)


TARGETS = {"arm64": "aarch64-apple-darwin", "amd64": "x86_64-apple-darwin"}
if len(sys.argv) != 2 or sys.argv[1] not in TARGETS:
    raise SystemExit("Usage: package-runner.py arm64|amd64")
arch = sys.argv[1]
target = TARGETS[arch]
out = ROOT / "bin"
packages = out / "packages"
packages.mkdir(exist_ok=True, parents=True)
target_dir = (ROOT / os.environ.get("CARGO_TARGET_DIR", "target")).resolve()
subprocess.run(["cargo", "build", "--locked", "--release", "--package", "bugsnitch-runner-journal",
                "--target", target, "--target-dir", target_dir], cwd=ROOT, check=True)
with tempfile.TemporaryDirectory(dir=out) as temp:
    stage = Path(temp)
    runner = stage / "bugsnitch"
    helper = stage / "bugsnitch-runner-journal"
    shutil.copy2(target_dir / target / "release" / helper.name, helper)
    subprocess.run(["go", "build", "-trimpath", "-o", runner, "./cmd/bugsnitch"], cwd=ROOT, check=True,
                   env={**os.environ, "CGO_ENABLED": "0", "GOOS": "darwin", "GOARCH": arch})
    destination = packages / f"bugsnitch-runner-darwin-{arch}.tar.gz"
    with tarfile.open(stage / destination.name, "w:gz") as archive:
        for path in (runner, helper, ROOT / "scripts/runner-service", ROOT / "LICENSE"):
            archive.add(path, arcname=path.name)
        package_notices(archive, target, arch)
    for binary in (runner, helper):
        os.replace(binary, out / f"{binary.name}-darwin-{arch}")
    os.replace(stage / destination.name, destination)
    print(destination.relative_to(ROOT))
