# Architecture

The Go HTTP service stores workspace-scoped desired state in PostgreSQL. A Vite/React application reads owner-authorized snapshots. The runner connects outbound. It owns its SQLite execution journal, Git worktrees and local model subprocess. GitHub owns ticket content, open state, PRs and required checks. Sentry supplies evidence about connected applications.

Owner sessions, pairing grants, device credentials, GitHub App installation tokens, plugin credentials and Codex sign-in have different authorities. The API authenticates actors before resolving a workspace. Pairing grants expire and can be redeemed once. Device assignments limit which project metadata a machine can publish. The local runtime owns model authorization. Hosted inference remains unavailable pending a documented applicable route.

A disconnected machine retains ownership of work. Last contact describes connectivity. Completion requires durable evidence from the owning runner. Raw logs, issue text, repository content and webhook payloads are untrusted inputs. Local execution assumes an owner-reviewed repository and validation configuration. A worktree shares Git metadata and the OS user.

## Local admission

The person starts a local run. On enrolled computers, each admission checks credential revocation, workspace, repository mapping, project pause and the approved criteria hash when a plan exists. A PostgreSQL transaction locks the workspace before reserving ticket capacity or a model permit. Waiting releases active execution capacity and retains the issue and shared locks. The runner journals pending requests and sequence numbers to reconcile a lost acknowledgment. A missed heartbeat never releases a reservation. Global limits apply only to enrolled processes participating in the protocol.

The dashboard reads persisted, bounded reports. An attempt version advances on the owning device and cannot change device, issue or base. Requests to control an attempt carry the reviewed version. A machine receives only typed operations bound to its current credential and assignments. Stop and pause remain requests until the runner acknowledges their disposition. Full worktree evidence stays local unless a specific field is included in the device protocol.

Owner proposals are immutable versions. Approval checks the current project version. A new plan cannot replace another plan while work remains owned. Dependency order uses priority then issue number. Owner-attested integration evidence unlocks prerequisites and reservations serialize declared workspace resources. Configured dates distinguish a target and an estimate. GitHub issue checks remain necessary at execution.
