# Implementation and acceptance

|Milestone|Exit evidence|
|---|---|
|0. Replacement and proof|Clean startup, migrated durable stores, real local account turn and versioned protocol|
|1. Single task|Canonical test issue through isolated execution, validation, independent review and real PR; restart and feedback exercise|
|2. Coordination|Concurrent identified runners, transactional dependency/capacity/lock checks, interruption and replay|
|3. Controlled delivery|Approved plan and ordered integration with a scoped human decision and stale-base protection|
|4. Interfaces|Shared MCP/dashboard operation, real installation instructions, keyboard and phone exercise|
|5. Incident repair|Duplicate intake to one ticket and repair; validated, deployed and environment-verified verdicts|
|6. Release|Clean install, authorization and recovery checks, backup restore, evidence inventory and implementation PR|

A provider fixture verifies a protocol contract. Live service acceptance requires a deliberately configured test project and separately recorded evidence. Work that needs a provider account may proceed independently of storage, local execution, dashboard and recovery checks.

## Rust migration

|Stage|Scope and evidence required|
|---|---|
|1. Storage compatibility|Ship the SQLite journal and PostgreSQL startup migrator in Rust. Open journals from the Go runner, retain existing migration digests and timestamps, exercise lost acknowledgments and concurrent migrations, and run backup/restore through the packaged service. See the [verification ledger](PROGRESS.md#rust-storage-migration).|
|2. Runner execution|Move named pause/stop journal operations, worktree lifecycle, validation and cancellation behind the Rust runner. See the [runner execution checkpoint](PROGRESS.md#rust-runner-execution). Exercise a real temporary Git checkout, owner feedback, interrupted validation and restart. Compare artifact and control-receipt formats before changing the CLI entry point.|
|3. Hosted coordination|Move the PostgreSQL claim, receipt, device and event paths into Rust. Exercise competing dispatchers, revocation, outbox replay, stale approvals and rolling compatibility with the prior runner and dashboard. See the [hosted coordination checkpoint](PROGRESS.md#rust-hosted-coordination).|
|4. Entrypoint cutover|Finish the local runtime adapter, hosted HTTP and integration handlers, then switch both entry points. Exercise the packaged GitHub issue workflow, MCP, deployment evidence and restore before retiring the Go implementations. External provider checks retain their separate prerequisites.|
