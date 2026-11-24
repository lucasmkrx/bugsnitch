#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Product driver for factory-eval.py. All issue reads use a labeled local fixture."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import time

HERE = Path(__file__).resolve().parent
REPOSITORY = "fixture/factory-eval"
ISSUE = "https://github.com/" + REPOSITORY + "/issues/1"


def json_file(path, value):
    temp = path.with_suffix(".tmp")
    temp.write_text(json.dumps(value) + "\n")
    temp.replace(path)


def validate_fixture():
    # This trusted wrapper runs the repository's visible tests on an export of
    # the current commit using the same OS sandbox as the independent grader.
    # It never receives the grader's acceptance inputs.
    sys.dont_write_bytecode = True
    spec = importlib.util.spec_from_file_location("factory_eval", HERE / "factory-eval.py")
    grader = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(grader)
    with tempfile.TemporaryDirectory(prefix="factory-validator-") as temp:
        source = Path(temp).resolve()
        repo = Path.cwd()
        grader.export_source(repo, grader.revision(repo, "HEAD"), source)
        result = grader.run_oracle(source, "test", supervised=True)
        return isinstance(result, dict) and result.get("count", 0) > 0 and result.get("failures") == result.get("errors") == 0


def fixture_gh(path, args):
    expected = ["api", "--hostname", "github.com", "--method", "GET", "repos/" + REPOSITORY + "/issues/1"]
    if args != expected:
        return 1
    sys.stdout.write(path.read_text())
    return 0


def main():
    if sys.argv[1:] == ["--validate-fixture"]:
        return 0 if validate_fixture() else 1
    if len(sys.argv) >= 3 and sys.argv[1] == "--issue-read":
        return fixture_gh(Path(sys.argv[2]), sys.argv[3:])
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default=str(HERE.parent / "bin" / "bugsnitch"))
    parser.add_argument("--provider", choices=["codex", "claude"], required=True)
    parser.add_argument("--rates", type=Path)
    args = parser.parse_args()
    job_file = Path(os.environ["FACTORY_JOB"]).resolve()
    job = json.loads(job_file.read_text())
    repo = Path.cwd().resolve()
    folder = job_file.parent
    if repo != (folder / "repo").resolve() or str(repo) != job["repository"]:
        raise ValueError("driver requires a factory-eval trial checkout")
    env = os.environ.copy()
    env["BUGSNITCH_HOME"] = str(folder / "runner")
    # Prevent accidental network reads through provider tokens or a signed-in gh.
    env.pop("BUGSNITCH_GITHUB_READ_TOKEN", None)
    env["BUGSNITCH_GITHUB_AUTH"] = "gh"
    env["PATH"] = str(folder / "bridge") + os.pathsep + env["PATH"]
    executable = str(Path(args.binary).resolve())
    state_path = folder / "bugsnitch-attempt.json"
    def git(*argv):
        return subprocess.check_output(["git", "-C", str(repo), *argv], text=True, env=env).strip()

    def cli(*argv):
        run = subprocess.run([executable, *argv], cwd=repo, env=env, capture_output=True, text=True)
        data = json.loads(run.stdout) if run.stdout.strip() else {}
        if data.get("id"):
            json_file(state_path, {"id": data["id"]})
        if run.returncode:
            # Preserve a bounded diagnostic outside the candidate checkout.
            (folder / "driver-error.txt").write_text(run.stderr[:4096])
            raise RuntimeError(argv[0] + " failed; see driver-error.txt")
        return data

    def snapshot_cost(attempt_id, completed=False):
        rate_args = ["--rates", str(args.rates.resolve())] if args.rates else []
        usage = cli("local-usage", "--id", attempt_id, *rate_args)
        json_file(folder / "usage.json", usage)
        amounts = [model.get("estimatedUSD") for model in usage["models"]]
        complete = completed and bool(amounts) and not usage["unmeteredTurns"] and all(v is not None for v in amounts)
        rows = {"source": "Bugsnitch local-usage; retained stage artifacts", "humanSeconds": None,
                "costUsd": sum(amounts) if complete else None, "costBasis": "estimated" if complete else None}
        json_file(Path(job["telemetryPath"]), rows)

    completed = False
    try:
        if os.environ["FACTORY_PHASE"] == "resume":
            saved = json.loads((folder / "checkpoint.json").read_text())
            attempt = cli("local-show", "--id", saved["id"])
            if (attempt["state"] != "waiting" or attempt["phase"] != "validate"
                    or attempt["candidateSha"] != saved["candidateSha"] or attempt["version"] != saved["version"]):
                raise ValueError("interrupted candidate changed before resume")
            summary = cli("local-continue", "--id", saved["id"], "--version", str(saved["version"]))
        else:
            original = str(folder.parent / "base")
            if git("remote", "get-url", "origin") != original or git("rev-parse", "HEAD") != job["base"]:
                raise ValueError("driver requires the original disposable clone")
            git("remote", "set-url", "origin", "https://github.com/" + REPOSITORY + ".git")
            # The product never publishes in these local trials.
            git("config", "remote.origin.pushurl", "disabled://factory-evaluation")
            bridge = folder / "bridge"
            bridge.mkdir(mode=0o700)
            stamp = "2026-01-01T00:00:00Z"
            title = "Factory checkout trial: " + job["task"]
            record = {"id": 1, "number": 1, "html_url": ISSUE, "state": "open", "title": title, "updated_at": stamp}
            json_file(bridge / "issue.json", record)
            command = [sys.executable, str(Path(__file__).resolve()), "--issue-read", str(bridge / "issue.json")]
            shim = bridge / "gh"
            shim.write_text("#!/bin/sh\nexec " + shlex.join(command) + ' "$@"\n')
            shim.chmod(0o700)
            source = {"schema": 1, "repository": REPOSITORY, "number": 1, "url": ISSUE, "state": "open",
                      "title": title, "updatedAt": stamp, "criteria": [job["ticket"]]}
            selection = folder / "selection.json"
            json_file(selection, source)
            stage = {"model": job["common"]["model"]}
            if args.provider == "claude":
                stage["provider"] = "claude"
            if job["common"].get("reasoning"):
                stage["reasoning"] = job["common"]["reasoning"]
            config = {"editDirectories": ["checkout", "tests"],
                      "stages": {key: stage for key in ("plan", "implement", "review")},
                      "validate": [sys.executable, str(Path(__file__).resolve()), "--validate-fixture"]}
            settings = folder / "worker.json"
            json_file(settings, config)
            cli("project-add", "--workspace", "personal", "--repository", REPOSITORY,
                "--path", str(repo), "--config", str(settings))
            summary = cli("local-start", "--repository", REPOSITORY, "--issue", str(selection))
            if summary.get("state") != "waiting" or summary.get("phase") != "implement":
                raise ValueError("expected the documented local plan checkpoint")
            extra = ["--until-candidate"] if job["task"] == "interrupted-ci" else []
            summary = cli("local-continue", "--id", summary["id"], "--version", str(summary["version"]), *extra)
            if extra:
                if summary["state"] != "waiting" or summary["phase"] != "validate" or not summary["candidateSha"]:
                    raise ValueError("worker did not reach the pre-validation checkpoint")
                json_file(folder / "checkpoint.json", summary)
                snapshot_cost(summary["id"])
                git("update-ref", job["checkpointRef"], summary["candidateSha"])
                while True:
                    time.sleep(60)
        while summary.get("state") == "waiting" and summary.get("reasonCode") == "review_correction":
            summary = cli("local-continue", "--id", summary["id"], "--version", str(summary["version"]))
        if summary.get("state") != "completed" or summary.get("phase") != "wrap_up":
            raise ValueError("worker stopped before independent review completed")
        git("update-ref", job["resultRef"], summary["candidateSha"])
        completed = True
    finally:
        if state_path.exists():
            snapshot_cost(json.loads(state_path.read_text())["id"], completed)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (KeyError, OSError, ValueError, RuntimeError, subprocess.SubprocessError) as exc:
        sys.exit(str(exc))
