# Working locally

Build the runner and its companion with `make runner`, or extract the macOS package for your architecture. Keep `bugsnitch` and `bugsnitch-runner-journal` in the same directory. Put Git and the supported Codex binary on PATH. Run `bin/bugsnitch doctor` from the build tree, or `./bugsnitch doctor` from the extracted package, to check the journal, execution and control protocol versions, bundled SQLite, Git and Codex. This release reports `capacity=1 execution=1 controls=1 attempts=1` on a compatible native helper. The runner uses the current OS account. Its local records live in `os.UserConfigDir()/bugsnitch` (on macOS, `~/Library/Application Support/bugsnitch`). A private absolute `BUGSNITCH_HOME` selects a separate runner identity and journal.

## Select a project

Select a trusted repository and check out the intended base revision. The runner records the current committed HEAD. Each attempt gets its own branch and linked checkout inside the runner's data directory. Existing uncommitted changes in the owner's checkout stay there. Check the checkout's `origin` matches the selected repository on github.com.

In the dashboard create a project and pairing code. Use the code and assigned project ID below. A second local registration records the smaller set of files the worker can receive and change, and the command the trusted validator may execute. This configuration lives outside the repository. Registering a checkout alone never enables model execution.

```sh
bin/bugsnitch pair --server https://bugsnitch.com --code PAIRING_CODE
bin/bugsnitch register --project PROJECT_ID --path /absolute/path/to/checkout
bin/bugsnitch project-add --workspace personal --repository owner/repo \
  --path /absolute/path/to/checkout --config /private/path/worker.json
```

Use the configured development origin for local testing. Pairing with a hosted service requires a deployed, reachable service. For a local-only run, use `project-add` without pairing. A paired device requires project assignment and execution admission from its registered service. Run `bin/bugsnitch sync --watch` in a separate terminal to refresh heartbeats, deliver bounded attempt metadata and receive controls. Device status is available with `bin/bugsnitch status`.

A reviewed worker configuration can look like this. Use an actual installed validation executable and acceptance check from the project. The command runs inside the new worktree.

```json
{
  "files": ["src/format.ts", "test/format.test.ts"],
  "validate": ["/absolute/path/to/node", "--test", "test/format.test.ts"]
}
```

Override the default four-minute turn timeout and ten-minute validation timeout in the reviewed JSON when the project needs longer runs. For example, `"stages":{"review":{"model":"MODEL_FROM_LOCAL_CATALOG","reasoning":"EFFORT_FROM_CATALOG","timeoutSeconds":480}}` changes independent review. `"validationTimeoutSeconds":1200` allows a twenty-minute validation. Model stage limits range up to one hour and the validation limit is four hours.

The current file-edit profile accepts existing tracked, non-hidden, regular UTF-8 files. Supply up to 32 files, 64 KiB per file and 128 KiB in total. The worker receives their contents, the issue selection and local feedback. Keep credentials, private reports and unrelated files out of the file list. Model credentials stay in the local Codex runtime. The validator receives a temporary home directory and a small environment. It runs as the OS user and project code can access resources that user can access. Choose a repository and validation configuration you trust. Use a separate OS user or disposable machine for additional isolation.

## Select and run a ticket

On an enrolled computer with a registered worker configuration, request the next verified issue from the latest approved plan. The command prints a selection only after the runner has checked the current GitHub issue. Use a private output path.

```sh
bin/bugsnitch next --project PROJECT_ID > /private/path/issue.json
```

The service considers priority 0 first and breaks ties by canonical issue number. It checks prerequisite integration evidence, resource locks, workspace and device capacity, repository authorization, the current plan binding and previous attempts. The runner then checks its own slots, approved checkout, issue ID, title, open state, timestamp and a digest of the canonical title and body. A successful recommendation reserves no capacity. Inspect the JSON before starting. The separately invoked `local-start` repeats GitHub verification and acquires admission at execution time.

Automatic recommendation currently covers tracked canonical issues from approved plans with a verified GitHub revision. A newly migrated record awaits its next provider read. A stale binding, unavailable private repository read, redacted canonical title or full allocation leaves stdout empty and returns a reason on stderr. Use the existing explicit retry workflow for an issue with a prior attempt.

Prepare a versioned issue selection from the GitHub issue and the owner's approved acceptance criteria. Copy the exact issue title, number, URL and API `updated_at` timestamp. The example values below are illustrative.

```json
{
  "schema": 1,
  "repository": "owner/repo",
  "number": 17,
  "url": "https://github.com/owner/repo/issues/17",
  "title": "Preserve leading zeros in the account display",
  "state": "open",
  "updatedAt": "2026-11-21T12:00:00Z",
  "criteria": ["The display preserves leading zeros", "The formatting regression test passes"]
}
```

At start, resume and wrap-up, the runner reads GitHub's Issues REST endpoint and checks the selection against its current revision. Pull requests, closed issues, moved issues and changed revisions fail verification. Public issues permit unauthenticated reads subject to GitHub rate limits. Private issues require a short-lived installation access token with Issues read access to the selected repository, supplied to the local process in `BUGSNITCH_GITHUB_READ_TOKEN`. Provisioning and refreshing that local read token is currently manual. The runner excludes it from model and validation subprocess environments. The selection contains owner-reviewed criteria. The current profile does not ingest arbitrary comments or attachments.

```sh
bin/bugsnitch local-start --workspace personal --repository owner/repo --issue /private/path/issue.json
bin/bugsnitch local-show --id ATTEMPT_ID
bin/bugsnitch local-continue --id ATTEMPT_ID --feedback "Keep the existing international display format."
bin/bugsnitch local-list
bin/bugsnitch sync
```

Inspect the plan before running `local-continue`. The implementation role proposes complete replacements of approved files. The runner verifies the worktree, writes only allowed paths, commits the candidate, and executes the registered validation argument vector. It records the actual exit code and checks the candidate for unintended changes. Runner-created candidate and integration commits suppress local Git hooks so hooks cannot introduce additional actions outside the registered validator. A project that relies on hook checks should include their reviewed validation commands in its configured validator. Required GitHub checks and protections still apply to publication and integration. The independent reviewer receives the actual diff, the issue and the recorded validation result in a fresh read-only thread. Important findings lead to a waiting state. Up to two owner-initiated corrections may follow. A successful wrap-up produces an inspectable candidate branch, exact commit and PR description in `PRBody` from `local-show`. Publishing or merging requires a separate authorized GitHub operation.

## Execute a bounded local batch

For a project with an approved plan and verified GitHub issues, the following example lets one enrolled Mac work on four independent tickets with up to two simultaneous model turns. In **Settings → Workspace capacity**, allow at least four active tickets, two workspace model turns and two turns on that device. Run these commands on the Mac with the matching CLI and journal helper installed.

```sh
bin/bugsnitch local-limits --tickets 4 --turns 2
bin/bugsnitch local-run --project PROJECT_ID --limit 4 --parallel 4
```

Run `bin/bugsnitch local-limits` to inspect the current settings. Both limits default to two, accept values from 1 to 32, and persist across processes sharing the same runner profile (`BUGSNITCH_HOME`). You can update either limit on its own. To run four model turns simultaneously, set `--turns 4` locally and allow at least four turns for both the workspace and the device. Other active work counts toward the same limits. Lowering a limit lets existing work finish and gates new reservations.

For a single invocation, `--limit` controls the total number of new tickets to select (1–32). `--parallel` controls how many attempts can run concurrently (1 through the configured local ticket limit). Both default to the local ticket limit. The runner checks the approved plan's dependencies and shared resources through the service, verifies each selected issue against GitHub, and waits for its admission before requesting another. Each attempt gets its own ID, branch, worktree and independent reviewer. A worker waiting for a model slot retains its ticket. It retries confirmed capacity conflicts for up to the configured stage timeout. Its model execution timeout starts when its turn begins. Use `sync --watch` or the sync service for dashboard updates.

A worker that requests an owner choice saves the question in `local-show --id ATTEMPT_ID` and waits. Answer with `local-continue --id ATTEMPT_ID --feedback "Your decision"`. A proposed edit to an identified sensitive path, including auth, permissions, migration, infrastructure, GitHub workflow and environment configuration paths, also needs feedback. These path checks cover named conventions; the registered trusted file list and validator still determine the execution boundary. Important review findings return to the owner before a correction. Running the batch again selects only new tickets and cannot approve a question, continue an old attempt, or authorize a correction.

The terminal emits one JSON record per observed admission or outcome. A waiting or cancelled outcome includes the reason. A failed admission or execution causes a nonzero exit after already admitted work settles. A service or GitHub read failure stops further selection. If an existing worker frees capacity, the same invocation can request another new ticket within its original limit. An interrupt cancels the foreground workers. No timer or server command starts another invocation. Successful output identifies local candidates; publication, merge and deployment retain their separate approval paths.

## Verify an issue on an existing deployment

Use `local-verify` when the code is already deployed and the issue needs observations. The command reads the open GitHub issue and records its identity, requirements digest, reviewed checklist, environment, build revision and one result per criterion in the local journal. It works from outside a checkout. Private issues can use `BUGSNITCH_GITHUB_READ_TOKEN` or the signed-in GitHub CLI. The verification run uses no model turn and creates no branch, commit or PR.

Write the reviewed criteria as a JSON array of strings. In this example, `criteria.json` contains `["A second team member can reopen the saved note"]`. For a manual walkthrough, write the observations to a file.

```json
[
  {
    "criterion": "A second team member can reopen the saved note",
    "result": "pending",
    "evidence": "The staging build responded. The second test account still needs to sign in."
  }
]
```

```sh
bin/bugsnitch local-verify --repository OWNER/REPO --number ISSUE_NUMBER \
  --criteria criteria.json --environment staging --revision "$DEPLOYED_REVISION" \
  --observer "Walkthrough operator" --observations observations.json
bin/bugsnitch local-verification --id RECEIPT_ID
```

Use an immutable deployment name, image digest or commit actually serving the tested environment. Each passed or failed row needs a concrete observation and a tested revision. Each pending row needs a reason. If every row is pending and the build is unknown, omit `--revision`. A failed row makes the overall result failed. Otherwise any pending row keeps it pending. The CLI prints the receipt for all three outcomes. Scripts that gate a release must inspect its `result` field. An invalid submission or unavailable issue returns a command error.

For an automated check, replace `--observations` with `-- /absolute/path/to/check` followed by its arguments. Bugsnitch sends the canonical issue, environment and revision as JSON on standard input. The trusted check inspects the target, then writes one JSON object with the observed `environment`, `revision` and `observations` array. It exits zero when it produced a valid report, even if an assertion failed. An unexpected exit, a target mismatch or an incomplete checklist cannot produce a passing receipt. The check runs in a temporary working directory with a private home. Pass any dedicated test configuration by file path and exclude credentials from observations.

Receipts distinguish a check launched by Bugsnitch (`runner-check`), manual observations (`operator`), and output imported from a named external check (`--source check --observer NAME --observations -`). A receipt stays on the runner until the operator shares it. Issue closure is a separate action.

## Recovery and retention

The SQLite journal keeps attempt identity, base and candidate revisions, approved files, versioned issue selection, worktree association and local artifacts. Duplicate starts for an existing issue return the existing ID. Use `local-retry --id ATTEMPT_ID --issue /private/path/current-issue.json --reason "Concrete reason"` for a failed or cancelled attempt when the problem has been addressed. Each explicit retry gets a separate ID, base, branch, worktree and evidence. The three-attempt budget includes the initial attempt. Repeating a prior retry returns the latest ID and does not start another worker. Concurrent continuations use an optimistic version check and per-attempt OS lock. Each stage acquires the required admission through the paired service. A model permit is released after the local app-server process exits.

A pause or stop receipt remains pending while a stage is active. The runner polls its SQLite mailbox, cancels the active model child or validation process group, and records quiescence after the call returns. Synchronous Git operations have a bounded timeout. Pausing preserves the attempt for explicit local continuation. Stopping produces a cancelled attempt. An unreachable or abruptly killed runner cannot confirm termination. A received control may remain pending until reconciliation. The validator must be safe to retry after an interrupted validation. Subprocesses that deliberately detach require OS-level isolation and separate inspection.

Use `local-show` and `git worktree list --porcelain` on the original computer after a hard crash. Never use a stored PID alone as evidence of process identity. Use `local-recover --id ATTEMPT_ID` when an implementation commit succeeded before its journal update. The runner acquires the attempt lock, checks the exact parent, paths, content digests and modes against the pre-commit intent, and returns a verified candidate to a waiting validation stage. Replaying recovery leaves the commit and artifacts intact. Partial edits and uncertain runtime stages require operator inspection on the original machine. Back up the private runner folder and the original Git repository together while the CLI and sync process are stopped. Use `local-cleanup --id ATTEMPT_ID` for completed or cancelled attempts. Cleanup verifies the path, branch, shared Git directory and revision and refuses dirty or ignored files. It removes only the clean linked checkout and retains the branch and journal. [Git worktree documentation](https://git-scm.com/docs/git-worktree)

## Verification

After the [focused test setup](../README.md#verification-and-operations), `go test -v ./internal/runner ./internal/agent` runs a real temporary Git repository and SQLite journal with labeled test doubles for GitHub issue reads and model outputs. The regression check fails on the broken baseline, passes on the committed candidate, and independently supplied review output is recorded against that candidate. Tests exercise admission subject binding, owner feedback, duplicate starts, recreation after a worktree side effect, dirty-worktree preservation, pending stop acknowledgment, command replay, credential exclusion, provider failure classes, commit-before-ack recovery, retention of ignored files during cleanup, bounded explicit retries and migration from the single-attempt journal. This fixture is evidence of the runner workflow. [Live integration status](CODEX.md)

## Draft a plan from a local brief

From the local terminal, run `bin/bugsnitch plan-draft --brief /private/path/brief.txt > proposal.json`. The planner receives that brief in a fresh read-only account-backed session. Its proposal includes a milestone, assumptions and uncertainty, distinct target/forecast dates, verifiable acceptance criteria, priorities, estimates, dependencies and resource locks. The CLI validates the result with the same domain rules as the hosted service. Ticket numbers in this output are provisional references within the proposal. Owner approval and publication must map them to actual GitHub issues. This command creates no GitHub tickets. To choose an available model and reasoning setting, add `--model` and `--reasoning` from the local model catalog.

Planning, manager conversations, authorization proofs and worker turns share the runner profile's model-turn limit. Paired computers also reserve a slot from the hosted workspace and device limits. The planner can draft before a project exists. A manager turn requires assignment to its selected project. The service checks enrollment and capacity and receives only the role, opaque turn identity, workspace/device identity, optional project identity and start/end state. It cannot initiate model execution through this endpoint. Paired general turns recheck the original grant every ten seconds, interrupting the local runtime on workspace stop, project pause, revocation or lost contact. An unreachable server can delay the final release acknowledgment.

Use `bugsnitch turns` to inspect unfinished local planning, manager and proof permits. After reconnection use `bugsnitch turn-reconcile --id PERMIT_ID` to replay an end acknowledgment or fence an acquisition that never started a model. If the local journal records a turn as running and the original CLI process has exited, confirm the runtime on that computer has terminated before using `--confirm-runtime-ended`. An OS lock prevents reconciliation while the owning CLI is still active. Lost contact alone keeps capacity reserved. A lost release response can be replayed with the original permit ID without another inference. Reconciliation of a revoked or replaced enrollment requires operator attention on the service.

## Active worker authority and lost acknowledgments

An enrolled worker verifies its original device, local repository mapping, issue, base SHA and admission sequence before a turn or validator. The service grants up to 15 seconds and the awake runner renews halfway through the remaining interval. A revoked credential, expired grant, matching stop or pause, suspended project, changed approved plan, or failed renewal cancels the active stage. The runner checks authority again before accepting successful output and before editing or committing. An interrupted validator records its actual exit when available. The worktree, candidate and partial edits remain available for inspection.

On macOS and Linux the Rust supervisor starts the approved command in a new process group with a private temporary home. A pause, stop, failed lease renewal, elapsed timeout or loss of the parent's control pipe ends validation. The supervisor signals the process group, verifies that its remaining foreground members have exited and reaps the direct child before returning an exit receipt. If the supervisor disappears before confirming the outcome, the attempt retains ownership and pending controls. Inspect the worktree and local processes on the original machine. Configure trusted foreground validators. A detached process, a validator with access to the owner’s credentials, or an external service it has already contacted can outlive the group. The runner cannot reverse those effects. The owner-initiated combined-candidate check on a completed attempt uses its own local timeout and sits outside worker admission.

The local SQLite journal retains confirmed end observations if an acknowledgment is lost. On reconnect, sync replays only the recorded ending operations under their original sequence and owning device. A held OS lock or changed local attempt version prevents background replay. The server keeps the original reservation while an end remains uncertain. Revoking the device can make that final acknowledgment impossible. A crash with an unconfirmed start or no observed process exit requires inspection on the original computer. Work never relaunches solely because a heartbeat, grant or acknowledgment expired.

A synchronized attempt includes a bounded reason code for missing sign-in, unavailable model, exhausted allowance, provider rate limit or service failure, admission or device denial, lost authority, local capacity, a pending control or ordinary task failure. Provider messages and credentials stay outside these records. A disconnected dashboard shows the last reported state and contact time. A newer local recovery reason becomes visible after a successful sync.

## Talk to the project manager locally

Write your question to a private text file, then run `bugsnitch manager --repository owner/repo --question /private/path/question.txt`. For idempotent retries, supply the same `--request-id` with 32 lowercase hexadecimal characters. The local SQLite conversation contains the actual owner message, final model reply, runtime thread and turn IDs, timestamps and completion state. Read it with `bugsnitch manager-history --repository owner/repo`. Only the current project's recent local conversation and bounded local attempt metadata enter model context. The role has read-only output and cannot change priority, create issues, approve decisions or merge.

The owner's local command starts each turn. Dashboard chat messages retain their hosted authorization status. No polling loop forwards a hosted prompt for local execution, and these local conversations remain on their original computer. An interrupted conversation may retain its last recorded start with no reply. Reusing that request ID returns its recorded disposition. A new question requires a new local action.

## Check a combined candidate

After a completed review, run `bin/bugsnitch local-integrate-check --id ATTEMPT_ID --target refs/heads/main`. A full `refs/remotes/origin/...` reference also works. Update remote-tracking references through your ordinary trusted Git workflow before selecting one. The runner creates a separate detached worktree at the exact selected target SHA, locally merges the reviewed candidate there, executes the registered validator and records the target, candidate and combined SHAs, exit code and issue revision. A stale ref or modified checkout invalidates the result. Read all receipts with `bin/bugsnitch local-integrations --id ATTEMPT_ID`.

The check retains the combined worktree for inspection. To explicitly remove a finished clean checkout, run `bin/bugsnitch local-cleanup --id ATTEMPT_ID --integration CHECK_ID`. Rust verifies its ownership marker, detached HEAD and clean index, keeps the combined commit reachable under `refs/bugsnitch/integration/CHECK_ID`, and records the cleanup in the receipt. Dirty, conflicted, unconfirmed or older unmarked checkouts require inspection. The original branches keep their revisions. The receipt applies to those exact revisions. It grants no merge authority. A service performing an actual merge still needs current policy, required checks, approvals and an enforceable base-freshness mechanism.

`TestIndependentlyPassingChangesFailInCombination` uses real temporary Git branches whose individual validators pass. Combining them violates a shared invariant. The runner records a failed combined validation while all original branches retain their tested revisions.

## Worker lease verification

The race-enabled runner tests interrupt a test model on revocation, observe a real validator process exit on renewal failure, retain its dirty worktree, and reject a renewal returned after expiry. Device tests simulate a lost end response and verify replay sends the original ending sequence without another start. The disposable PostgreSQL tests exercised competing general/worker capacity, the paired worker workflow and device-scoped renewal with stale sequences, matching stop controls, workspace pause and revocation.

## Plan delivery forecasts

The Plans view retains the owner's target and initial planning estimate. A separate forecast recalculates from evidence for the current approved version each time the service reads the plan. A ticket contributes its original estimated hours once the owner records an integrated revision and an acceptance summary through the plan evidence API. An open PR, a completed worker turn or a merged revision without that acceptance record contributes zero. Tracked GitHub issues must also retain their current authorized plan binding.

The projection uses calendar days in UTC, including the approval day and the current day. Divide completed estimate-hours by elapsed calendar days to obtain the observed pace, then divide remaining estimate-hours by that pace and round up to whole days. For example, integrating six of twelve estimate-hours over six calendar days gives six more calendar days at the same pace. Additional days without accepted work lower the measured pace. These are original scope weights, and forecast days include ordinary waits and past parallel work.

A projection requires accepted completions on at least two different UTC dates. An unapproved or superseded plan, stale GitHub evidence, or a paused project with unfinished work shows an explanation. Full acceptance shows the last recorded completion date. The target difference compares UTC dates. Positive values indicate a finish after the target. Later staffing, changed scope, new dependencies and future interruptions can change the outcome.
