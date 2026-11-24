# Paired factory evaluation

Run both products on fresh clones of the same commit with the same ticket, model, permissions, time limit and maximum spend. The evaluator measures the resulting **local candidate**. Each row identifies its driver as a real product or a test control. A successful local trial is evidence about candidate delivery. A production comparison also needs an independent deployment observation and an acceptance probe on the served build.

## Local trials

| Task | Outcome |
| --- | --- |
| Feature | Add a shipping fee module and a separate regression test. The public API must pass held-out cases. The new test must pass on the candidate and fail on the original public behavior. |
| Incident | Correct a shipping charge reported at the exact free-shipping threshold. Membership and neighboring subtotals remain covered. |
| Interrupted CI | Fix a committed failing test, publish the candidate checkpoint, and resume after the harness terminates the foreground driver process group. The final result must match that checkpoint and pass the independent checks. |

The script generates a new fee, threshold and held-out inputs for each pair. Both products start from the same commit within a pair. A seed reproduces those inputs, starting revisions and product ordering. The grader exports regular Python sources from the reported immutable commit, verifies its ancestry, and executes tests and behavior probes in a separate OS sandbox. macOS requires permission to start a system sandbox. On Linux, install Bubblewrap at `/usr/bin/bwrap` and run with a Python interpreter whose standard library and shared libraries are available under the normal system paths.

Build the local Bugsnitch runner and journal helper using `make runner`. Its bundled driver uses `project-add`, `local-start`, exact-version continuations and the normal independent review and validator. All issue reads go through a read-only, local fixture of the GitHub CLI. The driver keeps the trial journal and usage report beside the clone. Set `--provider codex` for a signed-in supported Codex runtime, with an explicitly supplied `CODEX_HOME` if needed. Use `--provider claude` with an explicitly passed `BUGSNITCH_ANTHROPIC_API_KEY` for the API. Both can consume account capacity or credits. Supply an optional `--rates /absolute/path/rates.json` to save cost estimates from available runner artifacts. Each trial uses the model in `common.model` and the optional reasoning setting in `common.reasoning`.

The bundled Zero driver requires a local checkout containing both the core and Cockpit plugins, a compatible Node executable and an explicitly selected Claude Code executable. It provisions a file tracker, a linked ticket worktree and the stock unattended policy under the trial’s private home. It invokes the native batch supervisor and reads its terminal ticket decision. The Git origin is the local benchmark seed repository and its push URL is disabled. Use a disposable worker identity with the necessary model access. A fixture executable may be used for a scripted control.

For matching live models, use the Claude provider on both sides with an exact model identifier supported by both installations and default reasoning. The Zero driver supports feature and incident trials. It records interrupted CI as unsupported because the adapter lacks a verified checkpoint that both stops before validation and reaps the worker.

The example uses absolute paths to both bundled drivers. Give each real run an exact build identifier.

```json
{
  "common": {
    "model": "claude-EXACT-SHARED-MODEL-ID",
    "permissions": "disposable repository only",
    "maxCostUsd": 2,
    "timeoutSeconds": 600
  },
  "bugsnitch": {
    "kind": "product",
    "build": "bugsnitch revision and local change digest",
    "argv": ["python3", "/absolute/path/to/bugsnitch/scripts/factory-bugsnitch.py", "--provider", "claude"],
    "passEnv": ["BUGSNITCH_ANTHROPIC_API_KEY"]
  },
  "zero": {
    "kind": "product",
    "build": "zero snapshot digest",
    "argv": ["python3", "/absolute/path/to/bugsnitch/scripts/factory-zero.py", "--zero", "/absolute/path/to/zero", "--node", "/absolute/path/to/node", "--worker", "/absolute/path/to/claude"],
    "passEnv": ["ANTHROPIC_API_KEY"]
  }
}
```

```sh
python3 scripts/factory-eval.py --drivers /private/path/drivers.json \
  --output /private/tmp/factory-evaluation --seed 7421 --repetitions 3
```

The output directory must be new. Each driver starts in its own clone with its own temporary and home directories. `FACTORY_JOB` identifies a JSON file containing the task, base SHA, ticket, repository, shared constraints, result reference, checkpoint reference and telemetry path. `FACTORY_PHASE` is `start` on first invocation and `resume` after the interruption. A driver can explicitly allow selected incoming environment variables using its `passEnv` array. Configure repeatable, identically scoped product identities and enforce the common spend ceiling through those accounts or adapters. The harness enforces the elapsed limit and checks reported spend after execution.

A wrapper submits the ticket to the named product, waits for it to produce a candidate, and makes its commit available in the supplied clone under `refs/heads/factory-result`. On the interrupted-CI trial it first records the reviewed commit in `refs/heads/factory-checkpoint` and stays alive. The harness kills the driver’s process group after observing the checkpoint and starts the same driver again. A wrapper should run the worker in the foreground or couple its worker’s lifetime to that foreground process. A persistent hosted worker needs a separately observed provider interruption to support a claim about remote-worker recovery. Set `unsupported` to a mapping of task names to reasons for capabilities the product lacks. A missing account or broken wrapper is an environment or trial failure.

Driver exit 75 identifies an explicit product hold. If a commit was produced, the harness grades it independently and records its result in `candidateGrade`. The aggregate retains the held outcome. Exit 78 identifies an unsupported driver capability. An optional `driver-outcome.json` beside the job file supplies a short `reason` for either outcome.

The wrapper may write an operator-measured timing and cost record to the supplied telemetry path. Include a source and specify `costBasis` as `billed` or `estimated`.

```json
{"costUsd":0.37,"costBasis":"billed","humanSeconds":42,"source":"trial-scoped billing export and operator timer"}
```

`report.json` is updated after each trial. It preserves failures and unsupported rows. Costs from unsuccessful attempts contribute to cost per passing candidate. Missing costs, mixed cost bases and missing human timings leave their respective aggregate unknown. Elapsed driver time and grading time are recorded separately for graded candidates. The output records actual model and permission declarations supplied by the operator. Check provider receipts before making claims about equal conditions. For a blinded product study, keep the harness and graders under a separate identity and retain a private historical task set. A seeded local smoke trial exercises a narrow family of checkout changes.

## Local product probes, November 24, 2026

The Bugsnitch runner suite passed the scoped feature flow, path scope checks and commit-before-acknowledgment recovery. The feature flow used scripted model output, a real Git worktree, the native validator and independent review stage. A separate macOS sandbox test read the fixture and wrote to scratch while unrelated-file reads and worktree writes failed.

Two paired controls ran the normal product CLIs on the same three generated tickets with seed 777 and one repetition per profile. Bugsnitch used scripted Codex responses, local GitHub issue fixtures and its shipped journal helper. Zero Cockpit 0.94.703 used its native batch supervisor, file tracker, a linked ticket worktree, the stock 43-rule unattended policy or that policy with a single validation permission, and its shipped scripted worker. Both test workers received prescribed edits. The run measured orchestration and validation behavior. Live model quality, active human time, deployment and cost remain unmeasured.

| Ticket | Bugsnitch, both profiles | Zero, stock policy | Zero, shared validator permission |
| --- | --- | --- | --- |
| Feature | Completed and passed independent grading | Held for operator verification. Candidate passed independent grading. | Completed and passed independent grading |
| Incident | Completed and passed independent grading | Held for operator verification. Candidate passed independent grading. | Completed and passed independent grading |
| Interrupted CI | Resumed at the recorded candidate after the driver group was killed. Candidate passed independent grading. | Unsupported by the evaluation driver | Unsupported by the evaluation driver |

For both held tickets, Zero located the candidate worktree and verification plan. Its stock policy required an operator to run `python3 -m unittest discover -s tests`. The harness preserved the supervisor’s waiting status even though the isolated grader accepted both commits.

Reproduce the control with both product checkouts, a local Node executable and the Bugsnitch CLI and journal helper built with `make runner`. The output directory must be new. Each invocation writes both scripted worker launchers, the driver configuration and the full per-ticket evidence there. The control runner passes no model credentials to the drivers.

```sh
python3 scripts/factory-controls.py --validation stock --seed 777 --zero /absolute/path/to/zero --node /absolute/path/to/node --output /tmp/factory-stock
python3 scripts/factory-controls.py --validation shared --seed 777 --zero /absolute/path/to/zero --node /absolute/path/to/node --output /tmp/factory-shared
```

The shared profile adds a single rule to Zero’s private trial allowlist. It approves `factory-public-tests`, a fixed, no-argument executable outside the checkout. It exports the candidate commit and runs public tests under the same OS sandbox and interpreter as the Bugsnitch validator. The harness separately tests held-out inputs against the submitted commit.

The local Zero snapshot also passed two focused board decisions covering continuation after CI and recovery after supervisor death, plus 18 worktree verification tests across `verify-checkouts-worktree.test.cjs` and `verify-checkouts-which-worktree.test.cjs`.

To run the Bugsnitch probes, configure the native journal helper as described in [RUNNER.md](RUNNER.md), then run:

```sh
go test -count=1 ./internal/runner -run '^(TestFeatureFromDiscoveredDirectory|TestDirectoryEditsRespectScope|TestCommitBeforeJournalAcknowledgment)$'
BUGSNITCH_TEST_ISOLATED_VALIDATOR=1 go test -count=1 ./internal/runner -run '^TestIsolatedValidator$'
```

From the Zero checkout, with a disposable home directory and absolute Node installation on PATH:

```sh
node --test --test-name-pattern='slice B: a cleared CI wait|a row still running under a supervisor that died' maintenance/zero-cockpit/city/ticket-next.test.cjs
node --test maintenance/zero-cockpit/engine/lib/verify-checkouts-worktree.test.cjs maintenance/zero-cockpit/engine/lib/verify-checkouts-which-worktree.test.cjs
```
