# Architecture

The Go HTTP service stores workspace-scoped desired state in PostgreSQL. A Vite/React application reads owner-authorized snapshots. The runner connects outbound. It owns its SQLite execution journal, Git worktrees and local model subprocess. GitHub owns ticket content, open state, PRs and required checks. Sentry supplies evidence about connected applications.

Owner sessions, pairing grants, device credentials, GitHub App installation tokens, plugin credentials and Codex sign-in have different authorities. The API authenticates actors before resolving a workspace. Pairing grants expire and can be redeemed once. Device assignments limit which project metadata a machine can publish. The local runtime owns model authorization. Hosted inference remains unavailable pending a documented applicable route.

A disconnected machine retains ownership of work. Last contact describes connectivity. Completion requires durable evidence from the owning runner. Raw logs, issue text, repository content and webhook payloads are untrusted inputs. Local execution assumes an owner-reviewed repository and validation configuration. A worktree shares Git metadata and the OS user.

## Local admission

The person starts a local run. On enrolled computers, each admission checks credential revocation, workspace, repository mapping, project pause and the approved criteria hash when a plan exists. A PostgreSQL transaction locks the workspace before reserving ticket capacity or a model permit. Waiting releases active execution capacity and retains the issue and shared locks. The runner journals pending requests and sequence numbers to reconcile a lost acknowledgment. A missed heartbeat never releases a reservation. Global limits apply only to enrolled processes participating in the protocol.

The dashboard reads persisted, bounded reports. An attempt version advances on the owning device and cannot change device, issue or base. Requests to control an attempt carry the reviewed version. A machine receives only typed operations bound to its current credential and assignments. Stop and pause remain requests until the runner acknowledges their disposition. Full worktree evidence stays local unless a specific field is included in the device protocol.

Owner proposals are immutable versions. Approval checks the current project version. A new plan cannot replace another plan while work remains owned. Dependency order uses priority then issue number. Owner-attested integration evidence unlocks prerequisites and reservations serialize declared workspace resources. Configured dates distinguish a target and an estimate. GitHub issue checks remain necessary at execution.

## One issue through review

1. A plan can bind existing issues or propose new ones. Publishing a proposal creates PostgreSQL write intents. Only confirmed GitHub issue numbers enter an execution plan. Material issue changes advance a generation and invalidate the prior binding.
2. A paired runner requests eligibility and independently rereads the selected GitHub issue. The local owner supplies a trusted checkout, allowed files and validation argv. PostgreSQL grants attempt ownership and serializes access to limited resources. SQLite records the base, branch, worktree and current stage.
3. Each local model turn requires its own permit. The implementer receives only the selected context. A trusted local component applies edits to approved files and runs the configured validator. A separate read-only review session receives the candidate diff, criteria and validation evidence.
4. The runner synchronizes stage metadata. Sharing a diff requires the owner to preview and approve its digest. After the dedicated branch is published, the service can open a draft PR through its App. A PR inspection binds observations to the head, target, scope and policy at inspection time. The retained local candidate and review artifacts identify the work independently of the PR.

The open-source local adapter is the only model execution path in this build. Dashboard messages preserve the owner's input with the applicable blocker. The local project-manager command keeps a bounded context in the runner's journal. The scope of an MCP grant limits which of the same hosted operations a compatible client can call. [Codex authorization](CODEX.md) · [Plugin setup](INTEGRATIONS.md)

## Reconciliation and evidence

GitHub and Sentry handlers validate the original signature, resolve an authorized installation and project, and persist the permitted event fields. Workers process accepted data and write intents under database coordination. A lost provider response leaves an uncertain intent. The integration checks the provider for its recorded marker before another write. Changes observed from GitHub can invalidate ticket eligibility and PR evidence.

Incident grouping preserves release and environment observations within one incident. A validated candidate, a deployment receipt and positive environment validation are distinct facts. Re-evaluation after a rollback or contradictory fixed-build occurrence preserves the earlier evidence. [Provider contracts](PROVIDERS.md) · [Pull-request and merge gates](INTEGRATION_QUEUE.md)

The hosted HTTP service is the only process that needs PostgreSQL and the GitHub App signing key. macOS devices hold their own scoped service credentials, project mappings and runtime association. The independent dashboard and ChatGPT bridge see only data allowed by their respective grants. Worktree isolation and credential limits are described in the [runner guide](RUNNER.md).
