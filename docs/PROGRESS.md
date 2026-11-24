# Rebuild verification ledger

The rebuild started on `rebuild/bugsnitch-v2` from clean main at `833637906ffc7d7de633b0de64e58c0636cd3bfd` in lucasmkrx/bugsnitch. The previous application was a TypeScript VS Code extension. Its history, MPL-2.0 license, trademarks and brand assets are preserved. [Implementation PR](https://github.com/lucasmkrx/bugsnitch/pull/24)

## Factory acceptance · November 24, 2026

| Exercise | Observed result |
| --- | --- |
| Checkout repair | Chromium reproduced the shipping error. The local runner made a candidate with scripted model responses. The validator passed, an independent review completed, and the staging fixture accepted signed deployment and behavior events for the served asset digest. Redeployment required a fresh browser check. A simulated recurrence on that digest held promotion. |
| Pull-request delivery | A disposable bare Git remote exercised initial publication, exact-head repair and undo receipts. Draft delivery recovered from a lost create response. A withdrawn approval could be renewed for a newer report, and delayed requests from the original approval were rejected. A loopback GitHub App exercised queue admission, retargeting, revocation, and refused merge groups. A two-thread deep review produced separate artifacts bound to the same validation and candidate. |
| Team controls | Packaged HTTP and PostgreSQL tests exercised project-scoped credentials, signed chat decisions, release ordering, monthly turn admission and estimated spend thresholds. A paired worker, planner and manager reported five provider-fixture turns through the normal device sync. Historical policy export preserved the pre-merge base and upgraded a clean label after an attributed recurrence. |
| Bugsnitch and Zero | With the same validator permission and scripted workers, both delivered feature and incident commits that passed held-out checks. Bugsnitch also passed the interrupted validation trial at the original SHA. The Zero driver lacks an equivalent tested checkpoint. |
| Installation and recovery | Two independent runner processes paired and synchronized. The packaged server survived restart and database restoration with session records intact. Chrome exercised the desktop and mobile workspace flows. |

Live GitHub rules, a real staging deployment, paid-model quality and comparative performance require trials with the intended provider configuration. [Queue enrollment](INTEGRATION_QUEUE.md) · [Provider setup](PROVIDERS.md) · [Factory evaluation and results](FACTORY_EVAL.md)

## Standalone single-owner acceptance · November 23, 2026

`make checkout-acceptance` starts its own PostgreSQL instance, runs the packaged service and local runner, and serves the bundled checkout on loopback. In the standalone run, Chromium reproduced the $5 charge at the free-shipping threshold. Two distinct SDK events and a replay produced one incident and one fixture issue. The runner used scripted model responses, applied the repair in a temporary Git worktree and ran the unchanged Node validator. Chromium confirmed free shipping on the served candidate. Redeploying identical bytes returned the incident to **Verification inconclusive** until a second browser check passed. An injected recurrence on the current build returned the incident to **Needs triage**. The previous live-model exercise is recorded below. [Reproduce the exercise](../examples/checkout/README.md)

PostgreSQL upgrade tests covered fresh and older databases, including deployment validations with ambiguous histories. The packaged service tests covered device pairing, browser and MCP access, restart, backup restoration and revocation. Both macOS runner archives were built. The Apple Silicon archive opened an empty profile and installed its LaunchAgent configuration into a temporary home.

The current build is unreleased. Live provider delivery, public TLS, hosted OAuth, and physical device interruption remain acceptance work. The existing GitHub CodeQL runs also have two open limitations. Result uploads were rejected for this private personal repository, and the pinned extractor reported partial Rust coverage. [GitHub scan run](https://github.com/lucasmkrx/bugsnitch/actions/runs/45249400205/job/137954707962) · [Code scanning eligibility](https://docs.github.com/en/code-security/concepts/code-scanning/code-scanning)

## Rust performance work

Baseline `f7733a3` (PR #33). This release moves frequent runner journal operations, dashboard reads, MCP lists and background job selection to Rust. The application keeps Go for CLI and model orchestration, small single-record MCP reads, application changes and provider-job consumers. The browser remains React and TypeScript. [Decision and boundaries](decisions/0008-performance-driven-rust.md)

| Local workload | Previous release | Candidate |
| --- | ---: | ---: |
| Dashboard state, 3 projects | 0.95 ms | 0.98 ms |
| Dashboard state, 60 projects | 4.99 ms | 6.56 ms |
| Dashboard state, 120 projects (about 5 MiB) | 38.27 ms | 33.29 ms |
| Dashboard diagnostics, 120 projects | 0.48 ms | 0.21 ms |
| Successful idle scheduler transactions per 16 seconds | 72 | 3 |
| Enqueue a due job through recording a fixture-provider failure | 1,434 ms | 44.5 ms |
| Sync one device with 32 acknowledged attempts | 4.66–9.77 ms | 0.67–0.69 ms |
| Read an attempt | 134–149 µs | 77–88 µs |
| Save an attempt checkpoint | 431.5 µs | 282.2 µs |
| Read 32 attempt summaries | 645.7 µs | 275.0 µs |
| MCP list of 100 attempts with five evidence records each | 2.26–2.81 ms | 1.43–2.18 ms |

Dashboard figures are warm medians of 31 authenticated loopback GETs per release and fixture after five warm-ups. The harness compares response values with identical data. The 60-project view took 1.57 ms longer. The 120-project response grew from 5,195,645 to 5,284,220 bytes due to JSON formatting, and its p95 fell from 39.27 ms to 34.20 ms. The state and diagnostics projections now use one scoped data query each, down from 15 and 9 respectively, excluding transaction setup. These figures describe the local datasets.

The scheduler benchmark recorded three 16-second windows after warm-up, then timed eight due drift jobs. PostgreSQL notifications carried empty payloads and the real consumer persisted the result from a local failing provider fixture. The previous scheduler made 72 successful idle calls in each window. The candidate made 3, 3 and 4.

The runner figures are medians of five one-second Go benchmarks on an Apple M5 with matching release helpers, real SQLite files and a local HTTP fixture. Get and sync show the range of medians across two series. The final alternating series also measured checkpoints and summaries. Save allocations measured in Go fell from 42,438 to 9,201 bytes per operation. The raw SQL comparison varied with host load (308 µs to 362 µs in the final series). The MCP ranges are medians of two runs per build over loopback with PostgreSQL 17 and a local scoped metadata grant. Each run used five warm-ups and 31 measured requests. The two releases returned the same number of response bytes on that fixture. Network latency to real providers and inference time are outside these workloads.

To repeat the comparison, keep matching binaries for each release in their own directory. Use a disposable local PostgreSQL instance with `PGHOST`, `PGPORT` and `PGUSER` set.

```sh
python3 scripts/benchmark-dashboard.py /path/to/previous/bin bin
python3 scripts/benchmark-mcp-reads.py /path/to/previous/bin bin
python3 scripts/benchmark-background.py bin
BUGSNITCH_JOURNAL_HELPER="$PWD/bin/bugsnitch-runner-journal" go test -run '^$' -bench BenchmarkJournal -benchmem -benchtime=1s -count=5 -p=1 ./internal/runner ./internal/device
```

Use this release’s runner and device benchmark fixtures in both checkouts when comparing older source. The synchronization fixture uses the real authorization filter, journal and request batching.

Local verification covered the full Rust and Go race-enabled suites, migration rollback and checksums, concurrent claims, expired sessions, cross-workspace isolation, offline control recovery, large snapshots, and rejection of unexpected fields in stored provider data. The release-upgrade exercise reused the original browser cookie, two paired runners and MCP bridge from `f7733a3`. Headless Chrome opened the restored incident and device views. The signed webhook and OAuth tests used local protocol fixtures. Both macOS package architectures built locally. Production serving, live provider traffic and inference performance remain unmeasured.

## Rust public HTTP and providers

This cutover began at `3f837c6`. The installed `bugsnitch-server` is Rust and serves the public REST API, dashboard, signed webhooks, hosted MCP and OAuth discovery. The GitHub App client is Rust. The private Go application worker still owns planning and policy transactions, decisions, dashboard projections and background scheduling. Both interfaces use the same durable records. [Architecture decision](decisions/0007-rust-http-and-providers.md) · [Installation](OPERATIONS.md#hosted-coordination-and-recovery)

- The full Rust and race-enabled Go suites passed with disposable PostgreSQL 17. The API contract suite now calls the packaged listener over loopback. It exercises scope checks, optimistic concurrency, emergency stop, issue selection, replay, artifacts and real runner subprocesses. A four-client cancellation test verifies that stalled native children are reaped before their slots can be reused.
- The GitHub fixtures exercised actual Rust HTTP requests, verified App signatures and token scope, tested pagination and rate-limit responses, and reconciled a lost write response. The packaged webhook and MCP exercise used signed Sentry/GitHub deliveries and a TLS OAuth introspection fixture. It covered redaction, duplicate delivery, revoked credentials, read-only grants and an authorized incident report. Vendor production traffic and an installed ChatGPT client were outside these exercises.
- An upgrade from the previous Go server preserved its owner cookie, enrolled runners and event cursors across restart and database restore. The earlier dashboard ran in Chrome at 360×800 and 1440×900 against both versions. The previous MCP bridge read the project and replayed a report with one durable incident. After restoring, the revoked runner received 403 and the other runner synchronized successfully. Use the `--initial-server`, `--runner` and `--mcp-bridge` options of the installation smoke to reproduce this check.
- Process tests exercised incomplete installations, worker death and abrupt parent exit. Rust asset tests covered conditional GET, byte ranges, deep links and symlink isolation. Both macOS runner archives built locally. GitHub has previously declined CodeQL uploads because code scanning is disabled on this private repository.

## Rust hosted coordination

Stage 3 began at `8b3fc23`. Rust now owns attempt admission, shared model permits, worker leases, eligibility queries, device operations, four provider queue reservations, incident processing and audit replay. The public service and provider calls still run in Go. The next migration stage covers the remaining hosted HTTP handlers and local model adapter. [Architecture decision](decisions/0006-hosted-coordination.md) · [Upgrade and recovery](OPERATIONS.md#hosted-coordination-and-recovery)

- The Rust suite and full race-enabled Go suite passed against disposable PostgreSQL 17. Tests covered the last capacity slot, plan dependencies, earlier criteria hashes, repeated commands, competing pairing recipients, revoked mappings, stale claims and rollback on an invalid acknowledgment. Concurrent reconnect, claim and emergency-stop tests used actual PostgreSQL lock waits.
- A transport fault fixture discarded the real Rust process's success reply after a device revocation committed. The API reported an unknown outcome. A fresh request recovered the recorded revocation with one audit event. Eight concurrent queue claims returned one reservation. Existing provider fixtures exercised uncertain external writes and reconciliation.
- A migration fixture upgraded historical events from two workspaces and preserved their event identities. The replay test committed two same-workspace events in the opposite order to their sequence numbers. A reader that had already consumed the first commit saw the later commit on the next page. A rolled-back event and a different workspace's event stayed out of the response.
- Two runner profiles built from `8b3fc23` paired, rotated credentials, registered projects and synchronized with the new service. The installation smoke exercised a restart, MCP, backup/restore, event cursor continuity, and selective revocation. Headless Chrome exercised the previous dashboard at 360×800 and 1440×900. The test used a local database and loopback HTTP. External GitHub, Sentry and hosted model acceptance retain their separate status below.
- The next-issue query reads capacity, approved scope and eligibility facts in four database reads, independent of plan size. A 64-ticket fixture with 1,024 dependency edges exercises redacted criteria, missing prerequisite evidence and a shareable candidate at the end of the list. The native protocol fixture exercises malformed and oversized frames, incompatible versions and recovery on a fresh request.
- GitHub reported code scanning disabled when CodeQL tried to upload results. The Linux and both macOS jobs are tracked on the [server migration PR](https://github.com/lucasmkrx/bugsnitch/pull/32).

## Rust runner execution

This stage started from `917cf2f` on 2026-11-22. The CLI now uses Rust for ordinary and detached worktree creation and verification, validation supervision, pause/stop receipt transitions and cleanup. The CLI's model turns, artifact assembly and hosted lease checks continue in Go. Install the CLI and companion from the same build. [Runner guide](RUNNER.md#recovery-and-retention)

- Real Git and SQLite tests exercised a complete local attempt, owner feedback, independent review with a model fixture, interrupted-commit recovery, and two independently passing candidates that failed when combined. Cleanup retained hidden edits, ignored files, mismatched registrations and the original branches. A separate SHA-256 Git repository exercise archived the exact tested combined commit after cleanup.
- Pause/stop receipts survive a stale settlement, a lost acknowledgment and replay of a previous release's pending stop. A validator launches against the approved revision. Separate tests cancelled the validator, let its timeout expire, and killed the Go runner. In all three cases the validator grandchild released a kernel lock and its private temporary directory was removed. Killing the Rust supervisor itself left the attempt owned, its exit code unknown and its stop request pending.
- The race-enabled Go suite, Rust tests and PostgreSQL migration fixture ran against a disposable database on macOS. Both macOS packages built. The installation smoke paired two runner processes, opened the dashboard in Chrome, exercised MCP, restored a backup and reconnected.
- Linux CI exposed a timeout race where the direct child had exited while its grandchild still held a file lock. After the fix the strict resource-release test passed on Linux, and the packaged runner suite passed on Intel and Apple Silicon macOS. Cancellation, timeout, owner death and ordinary leader exit also passed three consecutive race-enabled runs on the local Mac. [Cross-platform test run](https://github.com/lucasmkrx/bugsnitch/actions/runs/45029364179) A cross-version CLI check also read the same attempt, artifact and revision from both releases after a Rust stop and replay.
- GitHub code scanning remains unavailable for this private repository. Live model and vendor deployment traces remain tracked separately below.

## Rust storage migration

Stage 1 started from `ed6064e` on 2026-11-22. The runner now uses bundled SQLite through Rust for both its execution and device journals. The hosted service runs a Rust PostgreSQL migrator before accepting traffic. Both existing command names and stored formats are retained. The [migration plan](IMPLEMENTATION_PLAN.md#rust-migration) tracks the remaining Go runner and server work. GitHub reported that code scanning is disabled for this private repository when the PR workflow tried to publish its CodeQL results.

- The Rust tests and race-enabled Go suite passed against disposable PostgreSQL. The opt-in migrator fixture separately exercised fresh and existing ledgers, two competing starts, lock timeout, rollback, checksum mismatch and sanitized failure output.
- Journal tests opened an older SQLite database, preserved its attempt and receipt records, raced independent helper processes and reconciled a committed write whose acknowledgment was lost.
- The installation smoke paired and synchronized two runner profiles, used real MCP STDIO and HTTP, opened persisted records in Chrome, restarted the service and restored its PostgreSQL backup. GitHub Actions built and exercised the native runner archive on Intel and Apple Silicon macOS, including the service installer. A clean archive of the repository initialized its own database and served the dashboard with a successful readiness probe using `make dev`. [Native package checks](https://github.com/lucasmkrx/bugsnitch/actions/runs/45023772062)
- On an arm64 Mac, a committed journal update and read took a median **5.217 ms** with the previous CLI-backed journal and **0.291 ms** with the Rust helper. Each median covers five runs of 200 transactions on the same host, with setup excluded. The previous checkout used the identical `BenchmarkJournalTransaction` fixture. The comparison measures local journal latency, including the former subprocess startup. End-to-end task time and hosted throughput were outside this measurement.

## Milestones

|Milestone|Working implementation and evidence|Open acceptance work|
|---|---|---|
|0 · Repository and integration proof|Native development startup, transactional PostgreSQL migrations, two Go executables plus MCP bridge, React build, SQLite journal and versioned device protocol. A real local app-server STDIO turn returned the expected marker on ChatGPT sign-in.|OpenAI must establish the applicable authorization route for hosted-triggered inference. The service exposes its unavailable reason.|
|1 · One development task|User-selected current GitHub issue revision, clean independent worktree, local worker plan, owner feedback, implementation, actual validator exit, fresh review session, PR description and candidate. The optional real model fixture passed. An installed GitHub App can request a draft PR for an explicitly published matching branch.|Run once with a real selected issue and App in an owner-authorized disposable repository. Draft creation and uncertain-write recovery have protocol/database tests.|
|2 · Parallel execution and recovery|Two independent enrollments, atomic admission, workspace/device model capacity, dependency and resource checks, machine binding, outstanding receipt replay, local commit reconciliation, bounded revisions/retries, pause and stop acknowledgment, emergency controls, revocation and credential rotation.|The physical test used one Apple Silicon computer. A power-loss test on two physical machines remains open.|
|3 · Planning and controlled integration|Local planner and manager, evidence-based delivery forecasts, immutable versioned scope, issue publication outbox and dependency remapping, policy presets and overrides, scoped decisions, PR checks/queue observations and disposable combined-candidate validation. Two passing branches failed when combined in the regression exercise.|Exercise a live multi-issue project. Automatic enqueue needs an enforceable merge-group check bound to the authorized destination and deployment effects. Retargeting can preserve a PR head.|
|4 · Both interfaces and operations|Independent dashboard, on-demand redacted diffs, explicit demo, read-only and scope-gated MCP tools, real STDIO-to-HTTP calls and a shared PostgreSQL record. Chrome exercised phone/desktop navigation and stale decisions. Both macOS architecture binaries build.|Install and exercise the plugin in a configured ChatGPT host with its OAuth development tenant. LaunchAgent login lifecycle, Intel hardware execution and a public TLS host have separate live acceptance checks.|
|5 · Incident to verified repair|Signed transient ingress, allowlist and redaction, Sentry event deduplication, manual fingerprinting, per-release observations, policy-controlled issue promotion, one current issue/attempt owner, build-specific validation and deployment evidence. An instrumented checkout reproduced its shipping error in Chrome and a real local worker fixed it. Browser validation on the served candidate and subsequent recurrence were exercised. Database tests distinguish old-build errors, contradictory evidence, rollbacks and missing coverage.|Connect an actual Sentry test project and GitHub installation, then retain one continuous external trace from original provider event to deployed-build validation.|
|6 · Release readiness|Locked builds, native startup, readiness probes, request IDs, hashed credentials, versioned migrations, TTL content cleanup, scope/recovery tests, two enrollment profiles, dump/restore exercise and an implementation PR.|Production provisioning, DNS and domain TLS, vendor registrations, deployment testing and the checks above remain owner/environment work.|

## Live local evidence

On 2026-11-21 the supported local account path returned `BUGSNITCH_LOCAL_OK` using runtime `0.171.0-alpha.13` and model `gpt-6.2-sol`. The tested application never read model credentials. The adapter validated the account type, requested read-only sandbox, disabled connector capabilities for the thread and required a completed turn with the expected artifact. [Codex evidence](CODEX.md)

The elapsed-time fixture initially failed its regression. Real Codex planning and implementation turns produced candidate `6f885a997348adc8c2104474704b171b5399ffbb` from `cf92d631c397ca2ff8e5529accc700eb33000aaf`. The real validation process returned zero. Independent review ran in a separate model thread. The local attempt was `f50615b9cc7f76aa9651a164eccdec13`; the test finished in 37.18 seconds. Issue input was a protocol fixture. [Runner guide](RUNNER.md)

A clean clone at `c507d30` started from an empty cluster and served a signed-in owner an empty workspace using `make dev`. The native developer command initialized a private PostgreSQL 17.12 database, installed locked dependencies and served the built dashboard. A bridge extracted from the arm64 desktop plugin ZIP passed the installation smoke. The smoke exercised two independently launched runner profiles, an actual MCP client and service, a service restart, pg_dump/pg_restore into a fresh database, persisted owner sessions and revocation. Chrome signed in to the real service and viewed its incident and device records at 1440×900 and 360×800. The focused browser suite used explicit fixture responses to exercise lost responses, stale decisions and offline recovery. [Dashboard walkthrough and screenshots](DASHBOARD.md)

The checkout fixture produced two distinct browser SDK events and one promoted GitHub issue on the App protocol fixture. Local worker attempt `29cdf31f817d4c0f372efe558d78ad93` repaired baseline `81567744402e3724bfa310cf8d289b247c0cdbc6`. Candidate `dc60cf5d28912414c207192d7aec5dde7cb4ead3` passed the unchanged threshold regression and received independent model review. Chrome clicked through the served candidate build, yielding a positive environment-specific verdict. A later old-build event preserved the verdict. A synthetic contradictory event on the fixed build returned the incident to triage. The run took 37.93 seconds. The browser, SDK, database, worker, validation command and loopback deployment were real. Sentry backend routing and GitHub App responses used test fixtures. [Reproduce the checkout exercise](../examples/checkout/README.md)

The local race-enabled PostgreSQL suite passed, including forecast invalidation, authenticated event attribution, provider outbox recovery, credential rotation, retention and workspace-scoped diagnostics. Queue ages and stage durations come from actual recorded timestamps. The [Linux CI container exercise](https://github.com/lucasmkrx/bugsnitch/actions/runs/44940686737) built the hosted images, started Compose and verified service readiness. `bugsnitch next` rechecks scope, capacity and the current issue revision before recommending work. The bounded local dispatcher exercised two overlapping model-turn fixtures on distinct attempt IDs and Git worktrees, then executed real validators and respected a stale policy fence. `TestLocalRunTwoAttemptsAndFencedConflict` passed in 4.93 seconds. The forecast preserves the owner's target and reports insufficient evidence until it has observed integrated work on two distinct dates.

## Required failure scenarios

|Brief scenario|Evidence in the repository|
|---|---|
|1 · Duplicate and reordered delivery|TestDeviceReplayRevocationAndBinding and TestPairedWorkerAndDashboardEvidence check replay, version conflicts and ownership.|
|2 · Crash after effect before acknowledgment|TestCommitBeforeJournalAcknowledgment recovers an existing Git commit. TestPostgresReplayAndLostIssueResponse, TestOrderedPublicationLostResponse and TestPostgresDraftReconcilesLostResponseAndRejectsStalePolicy reconcile uncertain provider writes.|
|3 · Missed heartbeat|TestLocalTicketCapacityDoesNotReleaseLostContact and TestWorkerLeaseLossQuiescesValidatorAndRetainsWork preserve reservations and file evidence.|
|4 · Pending controls|TestControlsRemainPendingUntilQuiescence exercises a running subprocess, repeated receipt and observed exit.|
|5 · Final capacity slot|TestCompetingLocalClaimsAndPlanGates and TestGeneralAndWorkerTurnsShareOneBudget race real database transactions.|
|6 · Dependencies|TestPlanOrderAndCycle rejects invalid graphs. TestCompetingLocalClaimsAndPlanGates gates an approved plan on actual owner-recorded prerequisite evidence.|
|7 · Checkout isolation|TestDirtyAndSwappedWorktreesPreserved and TestCommitBeforeJournalAcknowledgment use real temporary Git repositories.|
|8 · Changed evidence|TestPostgresInvalidateEvidenceOnSubjectChange and TestPlannedIssueDriftCannotReviveStaleScope invalidate stale observations and approvals.|
|9 · Combined candidates|TestIndependentlyPassingChangesFailInCombination runs a real validator against the merged worktree. The integration writer has no direct merge fallback.|
|10 · Conflicting decisions|TestOwnerDecisionAndCrossInterfaceState checks stale subject versions and idempotent/conflicting answers.|
|11 · Authorization|TestOriginAndCredentials, TestDeviceReplayRevocationAndBinding, TestWorkerLeaseChecksOriginalSubjectAndCurrentAuthority and signed webhook fixtures cover origin, assignment, subject and installation checks.|
|12 · Provider and connectivity reasons|TestRemoteFailureClasses, TestWorkerReasonCodesAreBounded and TestUsagePreservesUnknownAndDropsAccountIdentity exercise distinct outcomes.|
|13 · Secret markers|TestOnlyApprovedErrorFieldsSurvive, TestOwnerDecisionAndCrossInterfaceState and TestDiffSharingRequiresCurrentRevisionAndExplicitDigest cover verified ingress, browser state, projected issue text and reviewable artifact sharing.|
|14 · Outage/retries|TestPostgresReplayAndLostIssueResponse covers durable canonical promotion. TestBoundedNewAttemptAfterExplicitRetry and issue reservations bound later attempts.|
|15 · Missing evidence|Incident fixtures preserve unknown builds and bounded stack metadata. Repair requires a canonical issue; reproduction evidence still requires an actual test case.|
|16 · Environment verification|TestPostgresReceiptsAndRecurrence and TestVerificationGates require a matching deployment and positive validation, including telemetry/exposure gates when configured.|
|17 · Phone, stale and offline|web/tests/dashboard.test.mjs exercises both requested viewports, keyboard focus, reconnect, fresh subject reads, and a lost approval acknowledgment.|
|18 · Clean setup and persistence|scripts/dev, scripts/smoke.py and web/tests/service-smoke.mjs exercise real processes, database, owner authentication, pairing, restart, restoration and browser navigation.|

## External acceptance

1. Select a disposable repository with Issues enabled. Install the GitHub App there, configure its client ID, private key file and webhook secret, and bind the verified installation/repository IDs to the project. Never use the implementation PR as a disposable merge test. [Provider guide](PROVIDERS.md)
2. Select one canonical test issue, approve the local project files and validation command, run the local issue workflow, publish its dedicated branch explicitly and request a draft. Inspect checks and combined evidence. [Runner guide](RUNNER.md) · [Integration gates](INTEGRATION_QUEUE.md)
3. Connect the deliberately instrumented checkout fixture to a Sentry test installation. Record a deployment and an actual positive build check before asserting verified behavior. [Fixture instructions](../examples/checkout/README.md)
4. Register the hosted OAuth connection and seek an applicable vendor decision on remote inference. Keep hosted-triggered model work unavailable in the meantime. [Integration inventory](INTEGRATIONS.md)
5. Before release, run the latest tests on the target Intel system, verify launchd across login/sleep, verify a TLS deployment and restore against a staging database, and refresh the signed provider/merge-group acceptance evidence.

The one-owner build retains local execution evidence by default. The hosted owner explicitly chooses any diff to share. Project targets and forecasts remain distinct. Production deployment execution, team invitations, billing and marketplace distribution are outside the verified inventory.
