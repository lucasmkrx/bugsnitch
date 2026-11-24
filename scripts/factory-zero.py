#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Run Zero Cockpit's shipped batch supervisor and file tracker in a fresh evaluation clone."""
import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys

TICKET = "EVAL-1"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--zero", required=True, type=Path, help="root of a Zero core and Cockpit installation")
    parser.add_argument("--node", required=True, type=Path)
    parser.add_argument("--worker", required=True, type=Path, help="explicit trial Claude Code executable")
    parser.add_argument("--provider", choices=("claude",), default="claude")
    parser.add_argument("--validation", choices=("stock", "shared"), default="stock",
                        help="optionally approve only the evaluator's fixed, sandboxed public test executable")
    args = parser.parse_args()
    job_path = Path(os.environ["FACTORY_JOB"]).resolve()
    job = json.loads(job_path.read_text())
    folder = job_path.parent
    repo = Path(job["repository"]).resolve()
    if repo != Path.cwd().resolve() or repo != folder / "repo":
        raise ValueError("expected a disposable clone created by factory-eval")
    root = args.zero.resolve(strict=True)
    node = args.node.resolve(strict=True)
    worker = args.worker.resolve(strict=True)
    cockpit = root / "plugins" / "zero-cockpit"
    auto = cockpit / "engine" / "autopilot.cjs"
    policy = cockpit / "scripts" / "unattended-policy.cjs"

    def finish(code, message):
        (folder / "driver-outcome.json").write_text(json.dumps({"reason": message}) + "\n")
        return code

    if job["task"] == "interrupted-ci":
        return finish(78, "This Zero driver has no tested stop-before-validation boundary that also reaps the worker.")
    if not job["common"]["model"].startswith("claude-") or job["common"].get("reasoning"):
        return finish(78, "This Zero driver requires an exact Claude model from the Cockpit catalogue and the default reasoning setting.")
    tools = folder / "commands"
    tools.mkdir()
    (tools / "claude").symlink_to(worker)
    (tools / "node").symlink_to(node)
    env = dict(os.environ, PATH=os.pathsep.join([str(tools), os.defpath]), ZERO_CORE_ROOT=str(root / "plugins/zero"),
               GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1", GIT_TERMINAL_PROMPT="0",
               GIT_ALLOW_PROTOCOL="file", GIT_AUTHOR_NAME="Factory trial", GIT_COMMITTER_NAME="Factory trial",
               GIT_AUTHOR_EMAIL="trial@example.invalid", GIT_COMMITTER_EMAIL="trial@example.invalid")
    for key in ("GITHUB_TOKEN", "GH_TOKEN", "SSH_AUTH_SOCK", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"):
        env.pop(key, None)

    def run(*cmd, capture=False):
        return subprocess.run(list(map(str, cmd)), cwd=repo, env=env, check=True, text=True,
                              capture_output=capture).stdout

    origin = Path(run("git", "remote", "get-url", "origin", capture=True).strip())
    if origin.resolve() != folder.parent / "base" or not origin.is_dir():
        raise ValueError("expected the evaluator's local origin")
    run("git", "config", "remote.origin.pushurl", os.devnull)
    worktree = repo / ".worktrees" / TICKET
    run("git", "worktree", "add", "-qb", TICKET, worktree, job["base"])
    channel = repo / ".claude"
    channel.mkdir()
    home = Path(env["HOME"]).resolve()
    if home != folder / "home" or Path(env["TMPDIR"]).resolve() != folder / "tmp":
        raise ValueError("expected the evaluator's private home and scratch directories")
    trusted = home / ".claude/zero/policy"
    trusted.mkdir(parents=True)
    prompt = (job["ticket"] + "\nUse only the prepared ticket worktree at " + str(worktree) +
              ". Commit the change there. Put the ticket plan in docs/plans/EVAL-1.md and include a "
              "'How this gets verified' section that runs the public Python suite. Keep delivery local.")
    if args.validation == "shared":
        prompt += " The exact command to run the approved sandboxed public suite is factory-public-tests."
    config = {"version": 1, "project": {"name": "factory-trial", "repos": [{"name": "."}]},
              "tracker": {"kind": "file", "file": ".claude/board.local.json", "ticketPrompt": prompt},
              "hosts": {"workers": "stdio", "sessions": "stdio", "unattendedAllowlist": str(trusted / "unattended-allowlist.json"),
                        "workerCeiling": str(trusted / "worker-ceiling.json")},
              "planning": {"planDirs": ["docs/plans"]}, "vcs": {"defaultBase": "main"}}
    (channel / "zero.config.json").write_text(json.dumps(config) + "\n")
    issue = {"id": TICKET, "title": "Factory " + job["task"], "description": job["ticket"],
             "state": "Todo", "stateType": "unstarted", "priority": 2, "estimate": 1, "labels": [], "blockedBy": []}
    (channel / "board.local.json").write_text(json.dumps({"me": "Trial", "issues": [issue]}) + "\n")
    with (folder / "supervisor.log").open("w") as log:
        subprocess.run([node, policy, "--write"], cwd=repo, env=env, check=True, stdout=log, stderr=log)
        if args.validation == "shared":
            # A single no-argument entry point outside the checkout. Both products export
            # the commit and run its public tests with the same sandbox and interpreter.
            public = tools / "factory-public-tests"
            validator = Path(__file__).resolve().with_name("factory-bugsnitch.py")
            public.write_text("#!/bin/sh\n[ \"$#\" -eq 0 ] || exit 64\nexec " +
                              shlex.join([sys.executable, str(validator), "--validate-fixture"]) + "\n")
            public.chmod(0o700)
            allowlist = trusted / "unattended-allowlist.json"
            rules = json.loads(allowlist.read_text())
            rules["rules"].insert(0, {
                "id": "factory-public-suite", "verdict": "allow",
                "when": {"tool": "Bash", "binary": public.name},
                "require": {"bareProgramName": True, "directoryInsideRepo": True, "redirectsInsideRepo": True},
                "why": "This trial approves the sandboxed public test executable."
            })
            allowlist.write_text(json.dumps(rules, indent=2) + "\n")
        for cmd in ([node, policy], [node, auto, "--tickets", TICKET, "--titles",
                    TICKET + "=" + issue["title"], "--prompt", prompt, "--model", job["common"]["model"],
                    "--concurrency", "1", "--minutes", str(max(1, (job["common"]["timeoutSeconds"] + 59) // 60))]):
            subprocess.run(list(map(str, cmd)), cwd=repo, env=env, check=True, stdout=log, stderr=log)
    # The first line is Cockpit's human-readable batch summary.
    status = run(node, auto, "--status", capture=True)
    state = json.loads(status.split("\n", 1)[1])
    row, = [item for item in state["tickets"] if item["id"] == TICKET]
    candidate = run("git", "rev-parse", "--verify", "refs/heads/" + TICKET + "^{commit}", capture=True).strip()
    if candidate != job["base"]:
        run("git", "update-ref", job["resultRef"], candidate)
    if row["status"] == "done" and row.get("outcome") == "succeeded":
        return 0
    message = "Zero supervisor " + row["status"] + ". " + str(row.get("note") or row.get("outcome") or "")
    return finish(75 if row["status"] == "waiting" else 1, message)


if __name__ == "__main__":
    raise SystemExit(main())
