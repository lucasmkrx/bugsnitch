"""Collect licenses for the Go modules linked into a Bugsnitch executable."""
from pathlib import Path
import shutil
import subprocess
import sys


def files(package, env=None):
    def go(*args):
        return subprocess.check_output(["go", *args], env=env, text=True).strip()

    goroot = Path(go("env", "GOROOT"))
    license_file = goroot / "LICENSE"
    if not license_file.is_file():
        license_file = goroot.parent / "LICENSE"  # Homebrew layout
    yield license_file, "go/LICENSE"
    linked = go("list", "-buildvcs=false", "-deps", "-f",
                "{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}", package)
    for module in sorted(set(linked.splitlines()) - {""}):
        name, version, directory = module.split(" ", 2)
        notices = sorted(p for p in Path(directory).iterdir() if p.is_file() and
                         p.name.upper().startswith(("LICENSE", "NOTICE", "COPYING", "UNICODE-LICENSE")))
        if not notices:
            raise ValueError(f"Missing distribution license for {name} {version}")
        for notice in notices:
            yield notice, f"{name}@{version}/{notice.name}"


if __name__ == "__main__":
    package, output = sys.argv[1:]
    for path, name in files(package):
        destination = Path(output) / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(path, destination)
