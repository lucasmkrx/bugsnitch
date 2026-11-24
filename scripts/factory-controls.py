#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Reproduce the paired orchestration controls using prescribed edits and no model accounts."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys

HERE = Path(__file__).resolve().parent
MODEL = "claude-haiku-5-5"  # A Cockpit catalogue row. Both workers are scripted.


def edits(ticket):
    fee, threshold = re.search(r"charge (\d+) when it is below (\d+)", ticket).groups()
    source = ("def quote(subtotal, member=False):\n"
              f"    return 0 if member or subtotal >= {threshold} else {fee}\n")
    if "new checkout/fees.py" not in ticket:
        return {"checkout/__init__.py": source}
    return {"checkout/__init__.py": "from .fees import quote\n", "checkout/fees.py": source,
            "tests/test_fees.py": "import unittest\nfrom checkout import quote\n\n"
            "class Fees(unittest.TestCase):\n    def test_boundary(self):\n"
            f"        self.assertEqual(quote({threshold}), 0)\n"}


def codex(argv):
    if argv == ["--version"]:
        print("codex-cli 0.171.0-alpha.13")
        return 0
    if argv[:1] != ["app-server"]:
        return 1
    thread = "control-" + str(os.getpid())
    turn = "control"
    directive = ""

    def send(message):
        print(json.dumps(message), flush=True)

    for line in sys.stdin:
        req = json.loads(line)
        if "id" not in req:
            continue
        method, params = req["method"], req.get("params", {})
        result = {}
        if method == "account/read":
            result = {"account": {"type": "chatgpt"}}
        elif method == "mcpServerStatus/list":
            result = {"data": []}
        elif method == "model/list":
            result = {"data": [{"id": MODEL, "model": MODEL, "isDefault": True}]}
        elif method == "thread/start":
            directive = params["developerInstructions"]
            result = {"thread": {"id": thread}, "cwd": params["cwd"], "sandbox": {"type": "readOnly"},
                      "model": MODEL, "modelProvider": "openai", "approvalPolicy": "never"}
        elif method == "turn/start":
            result = {"turn": {"id": turn}}
            if "Return scoped file operations." in directive:
                ticket = json.loads(params["input"][0]["text"])["issue"]["criteria"][0]
                changes = [{"operation": "replace" if name == "checkout/__init__.py" else "create",
                            "path": name, "destination": "", "content": body} for name, body in edits(ticket).items()]
                answer = json.dumps({"summary": "Implemented the prescribed rule", "edits": changes})
            elif "Independently inspect" in directive:
                answer = json.dumps({"summary": "Scripted control review", "findings": []})
            else:
                answer = "Plan to implement and verify the checkout behavior."
        send({"id": req["id"], "result": result})
        if method == "turn/start":
            send({"method": "item/completed", "params": {"threadId": thread, "turnId": turn,
                  "item": {"type": "agentMessage", "text": answer, "phase": "final_answer"}}})
            send({"method": "turn/completed", "params": {"threadId": thread, "turn": {"id": turn, "status": "completed"}}})
    return 0


def zero_worker(argv):
    profile, node, binary, *flags = argv
    if flags == ["--version"]:
        print("fixture fake-worker")
        return 0
    if flags == ["--help"]:
        print("--permission-prompt-tool --output-format --input-format")
        return 0
    job = json.loads(Path(os.environ["FACTORY_JOB"]).read_text())
    files = edits(job["ticket"])
    verify = "factory-public-tests" if profile == "shared" else "python3 -m unittest discover -s tests"
    fence = chr(96) * 3
    files["docs/plans/EVAL-1.md"] = ("# EVAL-1\n\n## Slices\n\nImplement the checkout rule.\n\n"
                                     "## How this gets verified\n\n1. Run the checkout suite.\n"
                                     f"{fence}bash\n{verify}\n{fence}\n\n## Decisions already made\n\n"
                                     "Use standard-library tests.\n")
    commands = ["cd .worktrees/EVAL-1", "mkdir -p docs/plans"]
    for name, body in files.items():
        commands.append("printf %s " + shlex.quote(body) + " > " + shlex.quote(name))
    commands.extend(["git add -- checkout tests docs/plans/EVAL-1.md", 'git commit -m "Implement local ticket"'])
    # The native Zero fake worker supports a scripted Bash tool with run=true.
    steps = [{"type": "init"}, {"type": "permission", "tool": "Bash", "input": {"command": "git status --short"}},
             {"type": "tool", "tool": "Bash", "input": {"command": " && ".join(commands)}, "run": True},
             {"type": "result", "text": "ZERO-DONE"}, {"type": "exit", "code": 0}]
    os.execv(node, [node, binary, "--scenario", json.dumps(steps), "--consume-prompt", *flags])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--zero", required=True, type=Path)
    parser.add_argument("--node", required=True, type=Path)
    parser.add_argument("--binary", default=HERE.parent / "bin/bugsnitch", type=Path)
    parser.add_argument("--output", required=True, type=Path, help="new directory for fixtures, trial states and results")
    parser.add_argument("--validation", choices=("stock", "shared"), required=True)
    parser.add_argument("--seed", type=int, default=777)
    parser.add_argument("--repetitions", type=int, default=1)
    args = parser.parse_args()
    zero, node, binary = [p.resolve(strict=True) for p in (args.zero, args.node, args.binary)]
    cockpit = zero / "plugins/zero-cockpit"
    fake = (cockpit / "engine/lib/hosts/fakes/fake-worker.cjs").resolve(strict=True)
    version = json.loads((cockpit / ".claude-plugin/plugin.json").read_text())["version"]
    out = args.output.resolve()
    out.mkdir(mode=0o700)
    tools = out / "commands"
    tools.mkdir()
    for name, subcommand in [("codex", ["codex"]), ("claude", ["zero-worker", args.validation, str(node), str(fake)])]:
        shim = tools / name
        shim.write_text("#!/bin/sh\nexec " + shlex.join([sys.executable, str(Path(__file__).resolve()), *subcommand]) + ' "$@"\n')
        shim.chmod(0o700)
    drivers = out / "drivers.json"
    drivers.write_text(json.dumps({
        "common": {"model": MODEL, "permissions": "private trial clone, " + args.validation + " validation",
                   "maxCostUsd": 0, "timeoutSeconds": 90},
        "bugsnitch": {"kind": "control", "build": "CLI SHA256 " + hashlib.sha256(binary.read_bytes()).hexdigest(),
                      "argv": [sys.executable, str(HERE / "factory-bugsnitch.py"), "--binary", str(binary), "--provider", "codex"]},
        "zero": {"kind": "control", "build": "Zero Cockpit " + version + ", shipped scripted worker",
                 "argv": [sys.executable, str(HERE / "factory-zero.py"), "--zero", str(zero), "--node", str(node),
                          "--worker", str(tools / "claude"), "--validation", args.validation]}}, indent=2) + "\n")
    env = {"PATH": os.pathsep.join([str(tools), os.defpath])}
    return subprocess.call([sys.executable, str(HERE / "factory-eval.py"), "--drivers", str(drivers),
                            "--seed", str(args.seed), "--repetitions", str(args.repetitions),
                            "--output", str(out / "trials")], env=env)


if __name__ == "__main__":
    if sys.argv[1:2] == ["codex"]:
        raise SystemExit(codex(sys.argv[2:]))
    if sys.argv[1:2] == ["zero-worker"]:
        raise SystemExit(zero_worker(sys.argv[2:]))
    raise SystemExit(main())
