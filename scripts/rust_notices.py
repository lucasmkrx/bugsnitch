"""Collect licenses for a locked Rust binary and its linked crates."""
import json
from pathlib import Path
import shutil
import subprocess
import sys


def files(crate_name, target=None):
    command = ["cargo", "metadata", "--locked", "--format-version", "1"]
    if target:
        command.extend(["--filter-platform", target])
    meta = json.loads(subprocess.check_output(command))
    packages = {p["id"]: p for p in meta["packages"]}
    nodes = {n["id"]: n for n in meta["resolve"]["nodes"]}
    pending = [next(key for key, p in packages.items() if p["name"] == crate_name)]
    visited = set()
    while pending:
        key = pending.pop()
        if key in visited:
            continue
        visited.add(key)
        pkg = packages[key]
        if pkg["source"]:
            root = Path(pkg["manifest_path"]).parent
            notices = sorted(p for p in root.iterdir() if p.is_file() and
                             p.name.upper().startswith(("LICENSE", "NOTICE", "COPYING", "UNICODE-LICENSE")))
            if not notices:
                raise ValueError(f"Missing distribution license for {pkg['name']}")
            for notice in notices:
                yield notice, f"{pkg['name']}-{pkg['version']}/{notice.name}"
        for dep in nodes[key]["deps"]:
            if any(kind["kind"] is None for kind in dep["dep_kinds"]):
                pending.append(dep["pkg"])
    toolchain = Path(subprocess.check_output(["rustc", "--print", "sysroot"], text=True).strip())
    yield toolchain / "share/doc/rust/COPYRIGHT-library.html", "rust/COPYRIGHT-library.html"


if __name__ == "__main__":
    crate, output = sys.argv[1:]
    for path, name in files(crate):
        destination = Path(output) / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(path, destination)
