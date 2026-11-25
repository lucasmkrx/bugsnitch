#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Replay the same event timelines for two product drivers and score paired evidence."""

import argparse
import collections
import importlib.util
import json
import math
import os
from pathlib import Path
import random
import statistics
import subprocess
import time

PRODUCTS = ("bugsnitch", "zero")
FACTORY = importlib.util.spec_from_file_location("factory_eval", Path(__file__).with_name("factory-eval.py"))
factory = importlib.util.module_from_spec(FACTORY)
FACTORY.loader.exec_module(factory)


def load(path):
    if path.stat().st_size > 1 << 20:
        raise ValueError("JSON input exceeds 1 MiB")
    return json.loads(path.read_text())


def command(config, key):
    args = config.get(key)
    if not isinstance(args, list) or not args or not all(isinstance(s, str) and s for s in args):
        raise ValueError(f"{key} needs an executable and argument list")
    return args


def details(file, case):
    result = load(file)
    if result.get("observedOutcome") not in ("verified", "hold", "incomplete"):
        raise ValueError("Observer must report verified, hold, or incomplete")
    if result.get("observedOutcome") == "verified" and not result.get("verifiedBuild"):
        raise ValueError("Verified outcomes require independent build evidence")
    for key in ("requiredApprovals", "avoidableInterventions", "duplicatePublications", "falseVerifiedReleases", "modelTurns"):
        if type(result.get(key)) is not int or result[key] < 0:
            raise ValueError(f"Invalid observer count for {key}")
    for key in ("operatorSeconds", "costUsd"):
        number = result.get(key)
        if number is not None and (type(number) not in (float, int) or not math.isfinite(number) or number < 0):
            raise ValueError(f"Invalid observer measurement for {key}")
    if result.get("costUsd") is not None and result.get("costBasis") not in ("billed", "estimated"):
        raise ValueError("Cost measurements require a basis")
    latencies = result.get("unblockLatencyMs")
    if not isinstance(latencies, dict) or any(
        key not in {event["id"] for event in case["events"] if event.get("unblock")}
        or type(value) not in (int, float) or not math.isfinite(value) or value < 0
        for key, value in latencies.items()
    ):
        raise ValueError("Unblock latencies must name measured unblock events")
    return result


def run_trial(config, product, case, repetition, folder, limits):
    folder.mkdir()
    row = {"product": product, "case": case["id"], "repetition": repetition, "expected": case["expected"]}
    if reason := config.get("unsupported", {}).get(case["id"]):
        return dict(row, status="unsupported", reason=reason)
    event_path = folder / "events.jsonl"
    metrics = folder / "observation.json"
    job = folder / "trial.json"
    factory.dump(job, {
        "case": case["id"], "expected": case["expected"], "ticket": case.get("ticket", ""),
        "product": product, "repetition": repetition, "common": limits,
        "events": str(event_path), "observation": str(metrics),
    })
    env = {key: os.environ[key] for key in config.get("passEnv", []) if key in os.environ}
    env.update(PATH=os.environ["PATH"], FACTORY_PROGRESSION_TRIAL=str(job))
    observer_env = {key: os.environ[key] for key in config.get("observerPassEnv", []) if key in os.environ}
    observer_env.update(PATH=os.environ["PATH"], FACTORY_PROGRESSION_TRIAL=str(job))
    with event_path.open("w", encoding="utf-8") as events, (folder / "driver.log").open("w") as log:
        start = time.monotonic()
        deadline = start + limits["timeoutSeconds"]
        with subprocess.Popen(command(config, "driver"), cwd=folder, env=env,
                              stdout=log, stderr=subprocess.STDOUT, start_new_session=True) as proc:
            try:
                for event in case["events"]:
                    ready = start + event["atMs"] / 1000
                    while proc.poll() is None and time.monotonic() < ready:
                        time.sleep(min(.05, max(0, ready - time.monotonic())))
                    if proc.poll() is not None:
                        break
                    events.write(json.dumps(dict(event, deliveredMs=round((time.monotonic()-start)*1000, 2))) + "\n")
                    events.flush()
                try:
                    code = proc.wait(timeout=max(0, deadline - time.monotonic()))
                except subprocess.TimeoutExpired:
                    return dict(row, status="failed", reason="driver timed out")
            finally:
                factory.stop_group(proc)
    if code == 78:
        return dict(row, status="unsupported", reason="adapter reported unsupported")
    if code not in (0, 75):
        return dict(row, status="failed", reason=f"driver exited {code}")
    if len(event_path.read_text().splitlines()) != len(case["events"]):
        return dict(row, status="failed", reason="driver exited before the replay completed")
    env_path = folder / "observer.log"
    with env_path.open("w") as log:
        subprocess.run(command(config, "observer"), cwd=folder, env=observer_env,
                       stdout=log, stderr=subprocess.STDOUT, check=True, timeout=limits["timeoutSeconds"])
    row.update(details(metrics, case))
    safe = row["duplicatePublications"] == 0 and row["falseVerifiedReleases"] == 0
    within_budget = row["modelTurns"] <= limits["maxTurns"] and (
        row.get("costUsd") is None or row["costUsd"] <= limits["maxCostUsd"])
    row["status"] = "passed" if safe and within_budget and row["observedOutcome"] == case["expected"] else "failed"
    row["unattended"] = row["status"] == "passed" and row["avoidableInterventions"] == 0
    return row


def percentile(items, fraction):
    ordered = sorted(items)
    return ordered[max(0, math.ceil(len(ordered) * fraction) - 1)] if ordered else None


def paired_report(rows, seed):
    by_pair = collections.defaultdict(dict)
    for row in rows:
        by_pair[(row["case"], row["repetition"])][row["product"]] = row
    comparable = [p for p in by_pair.values()
                  if set(p) == set(PRODUCTS) and all(r["status"] != "unsupported" for r in p.values())]
    eligible = [p for p in comparable if p["bugsnitch"]["expected"] == "verified"]
    differences = [int(p["bugsnitch"].get("unattended", False)) - int(p["zero"].get("unattended", False))
                   for p in eligible]
    confidence = None
    if len(differences) > 1:
        randomizer = random.Random(seed)
        draws = [statistics.mean(randomizer.choices(differences, k=len(differences))) for _ in range(2000)]
        confidence = [round(100*percentile(draws, q), 2) for q in (.025, .975)]
    products = {}
    for product in PRODUCTS:
        group = [p[product] for p in comparable]
        controls = [r for r in group if r["expected"] == "hold"]
        wins = sum(p[product].get("unattended", False) for p in eligible)
        verified = sum(r.get("observedOutcome") == "verified" and r["status"] == "passed" for r in group)
        costs = [r.get("costUsd") for r in group]
        basis = {r.get("costBasis") for r in group}
        products[product] = {
            "unattendedRate": wins / len(eligible) if eligible else None,
            "correctHolds": sum(r["status"] == "passed" for r in controls), "expectedHolds": len(controls),
            "verified": verified, "p95UnblockMs": percentile(
                [v for r in group for v in r.get("unblockLatencyMs", {}).values()], .95),
            "observedSafetyTrials": sum("duplicatePublications" in r for r in group),
            "requiredApprovals": sum(r["requiredApprovals"] for r in group)
            if group and all("requiredApprovals" in r for r in group) else None,
            "avoidableInterventions": sum(r["avoidableInterventions"] for r in group)
            if group and all("avoidableInterventions" in r for r in group) else None,
            "operatorSeconds": sum(r["operatorSeconds"] for r in group)
            if group and all(r.get("operatorSeconds") is not None for r in group) else None,
            "costPerVerified": sum(costs)/verified
            if verified and None not in costs and len(basis) == 1 else None,
            "duplicatePublications": sum(r.get("duplicatePublications", 0) for r in group),
            "falseVerifiedReleases": sum(r.get("falseVerifiedReleases", 0) for r in group),
        }
    return {
        "pairs": len(comparable), "eligiblePairs": len(eligible), "products": products,
        "unattendedDifferencePercentagePoints": round(100*statistics.mean(differences), 2) if differences else None,
        "paired95PercentInterval": confidence,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--drivers", type=Path, required=True)
    parser.add_argument("--scenarios", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--repetitions", type=int, default=3)
    parser.add_argument("--seed", type=int, default=7421)
    args = parser.parse_args()
    config = load(args.drivers)
    cases = load(args.scenarios)
    common = config["common"]
    if not (1 <= args.repetitions <= 100 and 1 <= common["timeoutSeconds"] <= 3600
            and 1 <= common["maxTurns"] <= 10000 and math.isfinite(common["maxCostUsd"])
            and common["maxCostUsd"] >= 0 and common.get("model") and common.get("permissions")):
        parser.error("use bounded repetitions and declare the model, permissions, time, turns, and spend")
    if not cases or len({case["id"] for case in cases}) != len(cases):
        parser.error("scenario IDs must be distinct")
    for product in PRODUCTS:
        command(config[product], "driver")
        command(config[product], "observer")
        if not config[product].get("build"):
            parser.error("declare both product builds")
    for case in cases:
        if case["expected"] not in ("verified", "hold") or not case["id"].replace("-", "").isalnum():
            parser.error("invalid scenario")
        events = case["events"]
        if len({event["id"] for event in events}) != len(events) or any(
            type(event["atMs"]) is not int or not 0 <= event["atMs"] < common["timeoutSeconds"]*1000
            for event in events
        ) or sorted(events, key=lambda e: e["atMs"]) != events:
            parser.error("event IDs must be distinct and scheduled in order inside the timeout")
    output = args.output.resolve()
    output.mkdir(mode=0o700)
    rows = []
    order = random.Random(args.seed)
    for repetition in range(1, args.repetitions + 1):
        for case in cases:
            for product in order.sample(PRODUCTS, len(PRODUCTS)):
                folder = output / f"{repetition:03}-{case['id']}-{product}"
                try:
                    row = run_trial(config[product], product, case, repetition, folder, common)
                except (KeyError, ValueError, OSError, subprocess.SubprocessError) as exc:
                    row = {"product": product, "case": case["id"], "repetition": repetition,
                           "expected": case["expected"], "status": "failed", "reason": str(exc)}
                rows.append(row)
                factory.dump(output / "report.json", {
                    "common": common, "seed": args.seed,
                    "builds": {p: config[p]["build"] for p in PRODUCTS},
                    "rows": rows, "paired": paired_report(rows, args.seed),
                })
    print(output / "report.json")


if __name__ == "__main__":
    main()
