#!/usr/bin/env python3
"""Build a matching macOS runner and journal executable and package them together."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile

from rust_notices import files as rust_notices

ROOT = Path(__file__).resolve().parents[1]


def output(*args):
    return subprocess.check_output(args, cwd=ROOT, text=True).strip()


def include_notices(archive, label, directory):
    files = sorted(p for p in directory.iterdir() if p.is_file() and
                   p.name.upper().startswith(("LICENSE", "NOTICE", "COPYING", "UNICODE-LICENSE")))
    if not files:
        raise ValueError(f"Missing distribution license for {label}")
    for notice in files:
        archive.add(notice, arcname=f"licenses/{label}/{notice.name}")


def package_notices(archive, target):
    for path, name in rust_notices("bugsnitch-runner-journal", target):
        archive.add(path, arcname="licenses/" + name)
    goroot = Path(output("go", "env", "GOROOT"))
    go_license = goroot / "LICENSE"
    if not go_license.is_file():
        go_license = goroot.parent / "LICENSE"
    archive.add(go_license, arcname="licenses/go/LICENSE")
    linked = output("go", "list", "-buildvcs=false", "-deps", "-f",
                    "{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}", "./cmd/bugsnitch")
    for line in sorted(set(linked.splitlines()) - {""}):
        name, version, path = line.split(" ", 2)
        include_notices(archive, name + "@" + version, Path(path))


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
        package_notices(archive, target)
    for binary in (runner, helper):
        os.replace(binary, out / f"{binary.name}-darwin-{arch}")
    os.replace(stage / destination.name, destination)
    print(destination.relative_to(ROOT))
