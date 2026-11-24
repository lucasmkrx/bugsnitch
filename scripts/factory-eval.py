#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Run paired local delivery trials. See docs/FACTORY_EVAL.md for the driver contract."""
import argparse
import collections
import json
import math
import os
from pathlib import Path
import random
import re
import resource
import secrets
import signal
import statistics
import subprocess
import sys
import tempfile
import time

TASKS = ("feature", "incident", "interrupted-ci")
PRODUCTS = ("bugsnitch", "zero")
RESULT_REF = "refs/heads/factory-result"
CHECKPOINT_REF = "refs/heads/factory-checkpoint"


def dump(path, value):
    temp = path.with_suffix(".tmp")
    temp.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    temp.replace(path)


def git(repo, *args, check=True):
    env = {"PATH": os.defpath, "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1",
           "GIT_CONFIG_COUNT": "0", "GIT_AUTHOR_DATE": "2025-01-01T00:00:00Z",
           "GIT_COMMITTER_DATE": "2025-01-01T00:00:00Z", "GIT_TERMINAL_PROMPT": "0", "LC_ALL": "C"}
    return subprocess.run(["git", "--no-replace-objects", "-c", "core.hooksPath=" + os.devnull,
                           "-c", "user.name=Factory evaluation", "-c", "user.email=eval@example.invalid",
                           "-C", str(repo), *args], env=env, check=check, capture_output=True, timeout=15)


def revision(repo, ref):
    result = git(repo, "rev-parse", "--verify", ref + "^{commit}", check=False)
    return result.stdout.decode().strip() if result.returncode == 0 else None


def seed_repo(repo, task, threshold, fee):
    repo.mkdir()
    git(repo, "init", "-q", "-b", "main")
    (repo / "checkout").mkdir()
    base = f"    return {fee}\n"
    if task != "feature":
        base = f"    return 0 if member or subtotal > {threshold} else {fee}\n"
    (repo / "checkout" / "__init__.py").write_text("def quote(subtotal, member=False):\n" + base)
    (repo / "tests").mkdir()
    (repo / "tests" / "test_shipping.py").write_text(
        "import unittest\nfrom checkout import quote\n\n"
        "class Shipping(unittest.TestCase):\n"
        f"    def test_standard_shipping(self):\n        self.assertEqual(quote(1), {fee})\n" +
        (f"    def test_threshold(self):\n        self.assertEqual(quote({threshold}), 0)\n"
         if task == "interrupted-ci" else ""))
    git(repo, "add", "--", "checkout", "tests")
    git(repo, "commit", "-qm", "Benchmark baseline")
    return revision(repo, "HEAD")


def ticket(task, threshold, fee):
    rules = (f"checkout.quote(subtotal, member=False) returns a fee in cents. "
             f"For a nonnegative integer subtotal, charge {fee} when it is below {threshold}. "
             "Members always receive free shipping. At and above the threshold, shipping is free.")
    if task == "feature":
        return rules + " Implement the rule in a new checkout/fees.py module and add tests/test_fees.py with a regression test."
    if task == "incident":
        return rules + f" A non-member customer was charged {fee} at exactly {threshold}. Repair the regression."
    return rules + " The committed CI regression test is failing. Repair it and recover the same candidate after a worker interruption."


def sandbox_command(source, scratch, program):
    # The independent oracle sees the exported Python files and the interpreter.
    # Its expected outputs and the other product's data live outside these roots.
    interpreter = Path(sys.executable).resolve()
    runtime = Path(sys.base_prefix).resolve()
    # Do not expose an entire system prefix (e.g. /usr or /) as an extra Python runtime.
    import sysconfig
    stdlib = Path(sysconfig.get_path("stdlib")).resolve()
    if str(runtime) in ("/", "/usr", "/usr/local"):
        runtime = stdlib
    if sys.platform == "darwin":
        profile = '''
(version 1)
(deny default)
(import "system.sb")
(allow process-exec sysctl-read)
(allow file-read-metadata (path-ancestors (param "TREE")) (path-ancestors (param "TEMP"))
 (path-ancestors (param "RUNTIME")) (path-ancestors (param "PYTHON")))
(allow file-read* file-map-executable (subpath "/System") (subpath "/usr/lib")
 (subpath (param "RUNTIME")) (literal (param "PYTHON")) (subpath (param "TREE")))
(allow file-read* file-write* (literal "/dev/null") (subpath (param "TEMP")))
'''
        return ["/usr/bin/sandbox-exec", "-D", "TREE=" + str(source), "-D", "TEMP=" + str(scratch),
                "-D", "RUNTIME=" + str(runtime), "-D", "PYTHON=" + str(interpreter),
                "-p", profile, str(interpreter), "-I", "-S", "-B", str(program)]
    if sys.platform.startswith("linux"):
        cmd = ["/usr/bin/bwrap", "--unshare-all", "--die-with-parent", "--new-session",
               "--cap-drop", "ALL", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp"]
        mounts = {str(stdlib), str(interpreter), "/usr/lib", "/lib", "/lib64", "/usr/lib64"}
        for path in sorted(mounts, key=len):
            if Path(path).exists():
                cmd += ["--ro-bind", path, path]
        return cmd + ["--ro-bind", str(source), "/work", "--chdir", "/work", "--",
                      str(interpreter), "-I", "-S", "-B", "/work/" + program.name]
    raise RuntimeError("the independent grader requires macOS sandbox-exec or Linux Bubblewrap")


def run_oracle(source, args, supervised=False):
    with tempfile.TemporaryDirectory(prefix="factory-scratch-") as temp:
        scratch = Path(temp).resolve()
        cmd = sandbox_command(source, scratch, source / "oracle.py")
        env = {"PATH": os.defpath, "HOME": str(scratch), "TMPDIR": str(scratch)}
        if sys.platform.startswith("linux"):
            env.update(HOME="/tmp", TMPDIR="/tmp")

        def limits():
            resource.setrlimit(resource.RLIMIT_CPU, (5, 5))
            resource.setrlimit(resource.RLIMIT_FSIZE, (64 * 1024, 64 * 1024))
            resource.setrlimit(resource.RLIMIT_CORE, (0, 0))

        with tempfile.TemporaryFile() as out:
            with subprocess.Popen(cmd, cwd=source, env=env, stdin=subprocess.PIPE, stdout=out,
                                  stderr=subprocess.DEVNULL, start_new_session=not supervised, preexec_fn=limits) as p:
                try:
                    p.communicate(json.dumps(args).encode(), timeout=10)
                finally:
                    if supervised and p.poll() is None:
                        p.kill()
                    elif not supervised:
                        stop_group(p)
            out.seek(0)
            raw = out.read(64 * 1024 + 1)
            if p.returncode or len(raw) > 64 * 1024:
                return None
            try:
                return json.loads(raw)
            except (ValueError, UnicodeError):
                return None


ORACLE = """import json, os, sys, unittest
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
request = json.load(sys.stdin)
if request == "ready":
    print(json.dumps("ready"))
elif request == "test":
    suite = unittest.defaultTestLoader.discover("tests")
    result = unittest.TextTestRunner(stream=sys.stderr).run(suite)
    print(json.dumps({"count": result.testsRun, "errors": len(result.errors), "failures": len(result.failures)}))
else:
    from checkout import quote
    print(json.dumps([quote(value, member) for value, member in request]))
"""


def export_source(repo, commit, source):
    records = git(repo, "ls-tree", "-rz", commit, "--", "checkout", "tests").stdout.split(b"\0")
    if len(records) > 129:
        raise ValueError("candidate exports more than 128 files")
    files = set()
    size = 0
    for record in filter(None, records):
        header, path = record.split(b"\t", 1)
        mode, kind, oid = header.split()
        name = path.decode()
        if not re.fullmatch(r"(checkout|tests)(/[a-zA-Z0-9_]+)+\.py", name):
            continue
        if kind != b"blob" or mode not in (b"100644", b"100755"):
            raise ValueError("candidate contains a non-regular Python file")
        size += int(git(repo, "cat-file", "-s", oid.decode()).stdout)
        if size > 256 * 1024:
            raise ValueError("candidate source exceeds 256 KiB")
        dest = source / name
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_bytes(git(repo, "cat-file", "blob", oid.decode()).stdout)
        files.add(name)
    (source / "oracle.py").write_text(ORACLE)
    return files


def grade(repo, base, candidate, task, threshold, fee, cases):
    if not candidate or candidate == base:
        return "no changed candidate on " + RESULT_REF
    if git(repo, "merge-base", "--is-ancestor", base, candidate, check=False).returncode:
        return "candidate is not based on the paired starting commit"
    with tempfile.TemporaryDirectory(prefix="factory-grade-") as temp:
        source = Path(temp).resolve()
        files = export_source(repo, candidate, source)
        if task == "feature" and not {"checkout/fees.py", "tests/test_fees.py"}.issubset(files):
            return "the new module or its regression test is missing"
        observed = run_oracle(source, cases)
        expected = [0 if member or amount >= threshold else fee for amount, member in cases]
        if (not isinstance(observed, list) or any(type(value) is not int for value in observed)
                or observed != expected):
            return "checkout behavior failed the independent oracle"
        tests = run_oracle(source, "test")
        if not isinstance(tests, dict) or not tests.get("count") or tests.get("errors") != 0 or tests.get("failures") != 0:
            return "candidate tests failed"
        if task == "feature":
            # The contributed test must detect the original defect on its own.
            (source / "checkout" / "__init__.py").write_bytes(
                git(repo, "show", base + ":checkout/__init__.py").stdout)
            # Exercise just the added test through the public checkout entry point.
            for path in (source / "tests").glob("test*.py"):
                if path.name != "test_fees.py":
                    path.unlink()
            control = run_oracle(source, "test")
            if not isinstance(control, dict) or not control.get("failures") or control.get("errors") != 0:
                return "the new regression test did not fail against the broken rule"
    return None


def stop_group(process):
    # poll before killing. An exited, reaped PID can be reused by an unrelated job.
    if process.poll() is None:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass


def driver(config, repo, env, deadline, checkpoint=False):
    # The operator-supplied wrapper owns any external jobs it launches. This
    # harness only interrupts the process group created for this invocation.
    with subprocess.Popen(config["argv"], cwd=repo, env=env, stdin=subprocess.DEVNULL,
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True) as proc:
        try:
            while time.monotonic() < deadline:
                if proc.poll() is not None:
                    return (proc.returncode, None)
                if checkpoint:
                    oid = revision(repo, CHECKPOINT_REF)
                    if oid:
                        return (None, oid)
                time.sleep(.1)
            raise TimeoutError("driver exceeded the common elapsed-time budget")
        finally:
            stop_group(proc)


def telemetry(path):
    out = {"costUsd": None, "humanSeconds": None}
    if not path.exists():
        return out
    raw = json.loads(path.read_text())
    for key in out:
        value = raw.get(key)
        if value is not None and (type(value) not in (int, float) or not math.isfinite(value) or value < 0):
            raise ValueError("invalid trial telemetry")
        out[key] = value
    source = raw.get("source")
    if any(v is not None for v in out.values()) and (not isinstance(source, str) or not source.strip()):
        raise ValueError("measured telemetry requires a source")
    basis = raw.get("costBasis")
    if out["costUsd"] is not None and basis not in ("billed", "estimated"):
        raise ValueError("cost telemetry requires a billed or estimated basis")
    out["costBasis"] = basis
    out["source"] = source
    return out


def trial(product, config, repo, base, task, threshold, fee, cases, common, ordinal, folder):
    text = ticket(task, threshold, fee)
    row = {"product": product, "task": task, "repetition": ordinal, "base": base,
           "status": "failed", "costUsd": None, "humanSeconds": None, "elapsedSeconds": None}
    reason = config.get("unsupported", {}).get(task)
    if reason:
        row.update(status="unsupported", reason=reason)
        return row
    job = folder / "job.json"
    metrics = folder / "metrics.json"
    dump(job, {"task": task, "ticket": text, "base": base, "repository": str(repo),
               "common": common, "resultRef": RESULT_REF, "checkpointRef": CHECKPOINT_REF,
               "telemetryPath": str(metrics)})
    env = {key: os.environ[key] for key in config.get("passEnv", []) if key in os.environ}
    env.update(PATH=os.environ["PATH"], HOME=str(folder / "home"), TMPDIR=str(folder / "tmp"),
               FACTORY_JOB=str(job), FACTORY_PHASE="start")
    for name in ("home", "tmp"):
        (folder / name).mkdir(mode=0o700)
    start = time.monotonic()
    try:
        deadline = start + common["timeoutSeconds"]
        interrupted = task == "interrupted-ci"
        code, pinned = driver(config, repo, env, deadline, interrupted)
        if interrupted and code not in (75, 78):
            if not pinned or pinned == base:
                raise ValueError("driver did not publish a changed checkpoint before interruption")
            row["interruptedAt"] = pinned
            env["FACTORY_PHASE"] = "resume"
            code, _ = driver(config, repo, env, deadline)
        outcome = folder / "driver-outcome.json"
        detail = "driver exit " + str(code)
        if code and outcome.is_file() and outcome.stat().st_size <= 16384:
            message = json.loads(outcome.read_text()).get("reason")
            if isinstance(message, str) and message.strip():
                detail = message[:2000]
        if code == 78:
            row["status"] = "unsupported"
            raise ValueError(detail)
        if code and code != 75:
            raise ValueError(detail)
        if code == 75:
            row.update(status="held", reason=detail)
        candidate = row["candidate"] = revision(repo, RESULT_REF)
        if pinned and candidate != pinned:
            raise ValueError("resume did not retain the interrupted candidate")
        row["elapsedSeconds"] = round(time.monotonic() - start, 3)
        grading_start = time.monotonic()
        failure = grade(repo, base, candidate, task, threshold, fee, cases)
        row["gradeSeconds"] = round(time.monotonic() - grading_start, 3)
        row["candidateGrade"] = failure or "passed"
        if failure:
            if code != 75:
                raise ValueError(failure)
        elif code == 0:
            row["status"] = "passed"
    except (ValueError, OSError, RuntimeError, subprocess.SubprocessError) as exc:
        row["reason"] = str(exc)
    finally:
        if row["elapsedSeconds"] is None:
            row["elapsedSeconds"] = round(time.monotonic() - start, 3)
    try:
        row.update(telemetry(metrics))
    except (ValueError, OSError, TypeError, AttributeError) as exc:
        row["telemetryError"] = str(exc)
    if row["costUsd"] is not None and row["costUsd"] > common["maxCostUsd"]:
        row.update(status="failed", reason="recorded spend exceeded the common budget")
    return row


def summary(rows):
    result = {}
    for product in PRODUCTS:
        group = [row for row in rows if row["product"] == product]
        passed = sum(row["status"] == "passed" for row in group)
        attempted = [row for row in group if row["status"] != "unsupported"]
        paid = [row for row in attempted if row["costUsd"] is not None]
        humans = [row["humanSeconds"] for row in attempted if row["humanSeconds"] is not None]
        elapsed = [row["elapsedSeconds"] for row in attempted]
        by_task = {}
        for task in TASKS:
            statuses = [row["status"] for row in group if row["task"] == task]
            by_task[task] = dict(collections.Counter(statuses))
        bases = set(row["costBasis"] for row in paid)
        result[product] = {
            "cases": len(group),
            "counts": dict(collections.Counter(row["status"] for row in group)),
            "byTask": by_task,
            "meteredAttempts": len(paid),
            "timedHumanAttempts": len(humans),
            "costBasis": next(iter(bases)) if len(bases) == 1 else None,
            "costPerPassUsd": (sum(row["costUsd"] for row in paid) / passed
                               if passed and len(paid) == len(attempted) and len(bases) == 1 else None),
            "humanSecondsPerPass": (sum(humans) / passed if passed and len(humans) == len(attempted) else None),
            "medianElapsedSeconds": statistics.median(elapsed) if elapsed else None,
        }
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--drivers", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--repetitions", type=int, default=3)
    parser.add_argument("--seed", type=int)
    args = parser.parse_args()
    cfg = json.loads(args.drivers.read_text())
    common = cfg["common"]
    if (not 1 <= args.repetitions <= 100 or not 1 <= common["timeoutSeconds"] <= 14400
            or not common["model"] or not common["permissions"]
            or not math.isfinite(common["maxCostUsd"]) or common["maxCostUsd"] < 0):
        parser.error("invalid repetition count, time budget, model, permissions or spend budget")
    for product in PRODUCTS:
        adapter = cfg[product]
        command = adapter.get("argv")
        if not isinstance(command, list) or not command or not all(isinstance(s, str) and s for s in command):
            parser.error(product + " requires a nonempty driver argv")
        if adapter.get("kind") not in ("control", "product") or not adapter.get("build"):
            parser.error(product + " requires a build identifier and a control or product driver kind")
        if any(task not in TASKS or not isinstance(reason, str) or not reason.strip()
               for task, reason in adapter.get("unsupported", {}).items()):
            parser.error("unsupported tasks need a known task name and a reason")
    with tempfile.TemporaryDirectory(prefix="factory-preflight-") as temp:
        source = Path(temp).resolve()
        (source / "oracle.py").write_text(ORACLE)
        if run_oracle(source, "ready") != "ready":
            parser.error("independent Python grader could not run in the OS sandbox")
    out = args.output.resolve()
    out.mkdir(mode=0o700, parents=True, exist_ok=False)
    seed = args.seed if args.seed is not None else secrets.randbits(64)
    rng = random.Random(seed)
    report = {
        "scope": "local immutable candidate behavior with synthetic checkout tasks",
        "drivers": {product: {k: cfg[product][k] for k in ("kind", "build")} for product in PRODUCTS},
        "seed": seed,
        "common": common,
        "repetitions": args.repetitions,
        "rows": [],
    }
    for ordinal in range(1, args.repetitions + 1):
        for task in TASKS:
            pair = out / f"{ordinal}-{task}"
            pair.mkdir()
            original = pair / "base"
            threshold, fee = rng.randint(2000, 9000), rng.randint(200, 900)
            amounts = [0, 1, threshold-1, threshold, threshold+1, 100000]
            amounts += [rng.randint(0, threshold * 2) for _ in range(12)]
            cases = [[amount, member] for amount in amounts for member in (False, True)]
            base = seed_repo(original, task, threshold, fee)
            sequence = list(PRODUCTS)
            rng.shuffle(sequence)
            for product in sequence:
                folder = pair / product
                folder.mkdir(mode=0o700)
                repo = folder / "repo"
                git(pair, "clone", "-q", "--no-local", str(original), str(repo))
                row = trial(product, cfg[product], repo, base, task, threshold, fee, cases, common, ordinal, folder)
                report["rows"].append(row)
                report["summary"] = summary(report["rows"])
                dump(out / "report.json", report)
                print(f"{ordinal} {task} {product} {row['status']}", flush=True)
    print(out / "report.json")


if __name__ == "__main__":
    main()
