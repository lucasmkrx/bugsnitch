#!/usr/bin/env python3
"""Assemble a portable desktop plugin ZIP for the selected Go OS and architecture."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
from zipfile import ZIP_DEFLATED, ZipFile, ZipInfo

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "integrations" / "chatgpt"


def go(*args, env=None):
    return subprocess.check_output(["go", *args], cwd=ROOT, env=env, text=True).strip()


def add(archive, name, source, mode=0o644):
    entry = ZipInfo(name, (1980, 1, 1, 0, 0, 0))
    entry.create_system = 3
    entry.external_attr = (0o100000 | mode) << 16
    entry.compress_type = ZIP_DEFLATED
    archive.writestr(entry, source.read_bytes())


def build():
    target = json.loads(go("env", "-json", "GOOS", "GOARCH", "GOROOT"))
    system, arch = target["GOOS"], target["GOARCH"]
    if system not in {"darwin", "linux"} or arch not in {"amd64", "arm64"}:
        raise SystemExit("Package a darwin or linux bridge for amd64 or arm64.")
    out = ROOT / "bin" / "plugins"
    out.mkdir(parents=True, exist_ok=True)
    destination = out / f"bugsnitch-{system}-{arch}.zip"
    env = {**os.environ, "CGO_ENABLED": "0"}
    # A fresh archive ensures a previous build cannot add stale binaries or local secrets.
    with tempfile.TemporaryDirectory(dir=out) as work:
        native = Path(work) / "bugsnitch-mcp"
        go("build", "-trimpath", "-buildvcs=false", "-o", str(native), "./cmd/bugsnitch-mcp", env=env)
        package = Path(work) / "plugin.zip"
        with ZipFile(package, "w") as archive:
            for name in ["plugin.json", "mcp.json", "skills/bugsnitch/SKILL.md"]:
                add(archive, name, SOURCE / name)
            add(archive, "bin/bugsnitch-mcp", native, 0o755)
            add(archive, "LICENSE", ROOT / "LICENSE")
            goroot = Path(target["GOROOT"])
            go_license = goroot / "LICENSE"
            if not go_license.exists() and goroot.name == "libexec":
                go_license = goroot.parent / "LICENSE"  # Homebrew distribution layout
            add(archive, "licenses/go/LICENSE", go_license)
            linked = go("list", "-buildvcs=false", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}", "./cmd/bugsnitch-mcp", env=env)
            for module in sorted(set(linked.splitlines()) - {""}):
                path, version, root = module.split(" ", 2)
                notices = sorted(p for p in Path(root).iterdir() if p.is_file() and p.name.upper().startswith(("LICENSE", "NOTICE", "COPYING")))
                if not notices:
                    raise RuntimeError(f"Missing distribution license for {path} {version}")
                for notice in notices:
                    add(archive, f"licenses/{path}@{version}/{notice.name}", notice)
        package.replace(destination)
    print(destination.relative_to(ROOT))


if __name__ == "__main__":
    build()
