# Working locally

Build the CLI from the repository root with `go build -o ./bin/bugsnitch ./cmd/bugsnitch`. Put Git, the SQLite CLI and the supported Codex binary on PATH. Run `bin/bugsnitch doctor` to check their versions. The runner uses the current OS account. Its local records live in `os.UserConfigDir()/bugsnitch` (on macOS, `~/Library/Application Support/bugsnitch`). A private absolute `BUGSNITCH_HOME` selects a separate runner identity and journal.

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

Inspect the plan before running `local-continue`. The implementation role proposes complete replacements of approved files. The runner verifies the worktree, writes only allowed paths, commits the candidate, and executes the registered validation argument vector. It records the actual exit code and checks the candidate for unintended changes. The independent reviewer receives the actual diff, the issue and the recorded validation result in a fresh read-only thread. Important findings lead to a waiting state. Up to two owner-initiated corrections may follow. A successful wrap-up produces an inspectable candidate branch, exact commit and PR description in `PRBody` from `local-show`. Publishing or merging requires a separate authorized GitHub operation.

## Recovery and retention

The SQLite journal keeps attempt identity, base and candidate revisions, approved files, versioned issue selection, worktree association and local artifacts. Duplicate starts for an existing issue return the existing ID. Use `local-retry --id ATTEMPT_ID --issue /private/path/current-issue.json --reason "Concrete reason"` for a failed or cancelled attempt when the problem has been addressed. Each explicit retry gets a separate ID, base, branch, worktree and evidence. The three-attempt budget includes the initial attempt. Repeating a prior retry returns the latest ID and does not start another worker. Concurrent continuations use an optimistic version check and per-attempt OS lock. Each stage acquires the required admission through the paired service. A model permit is released after the local app-server process exits.

A pause or stop receipt remains pending while a stage is active. The runner polls its SQLite mailbox, cancels the active model child or validation process group, and records quiescence after the call returns. Synchronous Git operations have a bounded timeout. Pausing preserves the attempt for explicit local continuation. Stopping produces a cancelled attempt. An unreachable or abruptly killed runner cannot confirm termination. A received control may remain pending until reconciliation. The validator must be safe to retry after an interrupted validation. Subprocesses that deliberately detach require OS-level isolation and separate inspection.

Use `local-show` and `git worktree list --porcelain` on the original computer after a hard crash. Never use a stored PID alone as evidence of process identity. Use `local-recover --id ATTEMPT_ID` when an implementation commit succeeded before its journal update. The runner acquires the attempt lock, checks the exact parent, paths, content digests and modes against the pre-commit intent, and returns a verified candidate to a waiting validation stage. Replaying recovery leaves the commit and artifacts intact. Partial edits and uncertain runtime stages require operator inspection on the original machine. Back up the private runner folder and the original Git repository together while the CLI and sync process are stopped. Use `local-cleanup --id ATTEMPT_ID` for completed or cancelled attempts. Cleanup verifies the path, branch, shared Git directory and revision and refuses dirty or ignored files. It removes only the clean linked checkout and retains the branch and journal. [Git worktree documentation](https://git-scm.com/docs/git-worktree)

## Verification

`go test -v ./internal/runner ./internal/agent` runs a real temporary Git repository and SQLite journal with labeled test doubles for GitHub issue reads and model outputs. The regression check fails on the broken baseline, passes on the committed candidate, and independently supplied review output is recorded against that candidate. Tests exercise admission subject binding, owner feedback, duplicate starts, recreation after a worktree side effect, dirty-worktree preservation, pending stop acknowledgment, command replay, credential exclusion provider failure classes, commit-before-ack recovery retention of ignored files during cleanup, bounded explicit retries and migration from the single-attempt journal. This fixture is evidence of the runner workflow. [Live integration status](CODEX.md)
