#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Summarize a local implementation trial from a bugsnitch local-show export."""

import argparse
import json


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("attempt", type=argparse.FileType("r"), metavar="ATTEMPT_JSON",
                        help="a local-show JSON file, or - for stdin")
    parser.add_argument("--issue-source", choices=("github", "fixture", "unknown"), default="unknown")
    for role in ("implementation", "review"):
        parser.add_argument(f"--{role}-source", choices=("live-model", "scripted", "unknown"),
                            default="unknown", help=f"source of the {role} response in this trial")
    args = parser.parse_args()
    try:
        attempt = json.load(args.attempt)
        candidate = attempt.get("candidateSha")
        # A correction can leave a previous candidate's successful review
        # in the same journal. Report only artifacts for the selected SHA.
        phases = {
            artifact["Phase"]: artifact
            for artifact in attempt["Artifacts"] or []
            if candidate and artifact.get("CandidateSHA") == candidate
        }
        result = attempt["state"]
        if result == "completed" and attempt["phase"] == "wrap_up" and candidate:
            result = "Local candidate ready"
        exit_code = phases.get("validate", {}).get("ExitCode")
        report = {
            "attempt": attempt["id"],
            "issue": {
                "repository": attempt["repository"],
                "number": attempt["issueNumber"],
                "source": args.issue_source,
            },
            "outcome": result,
            "phase": attempt["phase"],
            "candidateRevision": candidate,
            "validatorExitCode": exit_code,
        }
        for role, phase in (("implementation", "implement"), ("review", "review")):
            if phase in phases:
                report[role] = {
                    "status": "recorded",
                    "source": getattr(args, f"{role}_source"),
                    "reportedModel": phases[phase].get("Model") or None,
                }
            elif phase == "review" and attempt["phase"] == "validate" and exit_code not in (None, 0):
                report[role] = {"status": "not_run"}
            else:
                report[role] = {"status": "no_artifact"}
    except (ValueError, KeyError, TypeError, AttributeError) as exc:
        parser.error(f"expected a local-show implementation attempt: {exc}")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
