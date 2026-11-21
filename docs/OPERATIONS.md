# Operations

Run one hosted service behind HTTPS with PostgreSQL on a private network. Use an independent database for tests. The owner bootstrap secret is a high-entropy login credential. Rotate it and invalidate existing sessions if compromised. Revoke device credentials from the dashboard when a computer is retired.

Keep database backups encrypted. Restore a logical backup into a fresh database and verify workspace, project, incident and event counts before switching traffic. Local worktrees and SQLite journals require separate device backups. Database backups do not contain uncommitted work.

## Hosted service

The native startup is documented in README.md. The container alternative uses `cp deploy/example.env .env`, three independent hex secrets as needed, then `docker compose --env-file .env -f deploy/compose.yml up --build`. The service binds to loopback; PostgreSQL stays on the Compose network. Use a private link with certificate verification for external PostgreSQL. Set BUGSNITCH_ORIGIN to the public HTTPS origin, including the correct port where applicable. BUGSNITCH_LISTEN controls the HTTP listener. Set BUGSNITCH_WEB_DIR for an alternate web/dist location.

For bugsnitch.com, provision the database and host, route the domain to that host after owner approval and terminate TLS with the supplied Caddyfile or an equivalent HTTPS proxy. Set proxy timeouts and request limits appropriate to webhook size. Use a secret manager for DATABASE_URL, BUGSNITCH_OWNER_TOKEN, GitHub App and Sentry secrets. Inject GITHUB_APP_CLIENT_ID, GITHUB_APP_KEY_FILE, GITHUB_WEBHOOK_SECRET and SENTRY_CLIENT_SECRET only on the server. Mount the PEM read-only for the service user. Provider mappings and callback setup are described in PROVIDERS.md. No DNS or production changes were performed.

The hosted bootstrap credential belongs to the owner. Browser sessions use hashed opaque secrets with a 12-hour expiry. Cookies use HttpOnly, SameSite Strict and Secure on HTTPS. Changing BUGSNITCH_OWNER_TOKEN affects future sign-in; deleting this owner's sessions forces sign-in again. Connections from devices use a separate 256-bit local credential, with only its digest stored centrally. Use `bugsnitch rotate` to replace a device credential while preserving its identity and reservations. The old credential stops authorizing sync immediately. A lost rotation acknowledgment can be reconciled for 24 hours using the recorded pending key. Revoke and pair afresh if that window expires. Revocation blocks subsequent sync and admission. A sleeping computer may retain uncommitted work. Confirm quiescence on that computer before recovering reservations.

To enable GitHub in Compose, set GITHUB_APP_KEY_PATH to the absolute PEM path on the host. Grant read access to container UID 10001 using a read ACL or dedicated file ownership. Keep the key outside the build context. Set the client ID and webhook secret, then run:

```sh
docker compose --env-file .env -f deploy/compose.yml -f deploy/github-app.yml up -d --build
```

The optional override mounts the key read-only. The base compose file passes the Sentry secret and hosted MCP OAuth settings when supplied. Register each callback and project mapping separately. [Provider setup](PROVIDERS.md)

## Install the macOS runner

Run `make macos` to build arm64 and amd64 binaries. Select the matching binary and run `scripts/runner-service install bin/bugsnitch-darwin-arm64` or the amd64 equivalent. Pair and register under the same normal user. Run `scripts/runner-service start` to run the sync loop at login, `scripts/runner-service status` to inspect launchd and `scripts/runner-service stop` to unload it. Service installation is explicit. Local execution still needs the approved repository, Git, the SQLite command-line program, the supported Codex binary and the project's validation toolchain. Closing the ChatGPT window does not itself terminate the separate CLI process. Sleep and network loss suspend contact.

## Update, backup and restore

1. Stop new dispatch, reconcile active local work and record the deployed software revision. Take a PostgreSQL custom-format backup with `pg_dump -Fc --no-owner -f bugsnitch.dump "$DATABASE_URL"`. Protect the dump as sensitive workspace data.
2. Restore into an empty, isolated database with `pg_restore --exit-on-error --no-owner --dbname="$RESTORE_DATABASE_URL" bugsnitch.dump`. Disable provider credentials while verifying the restored system, to avoid running two writers for the same installation.
3. Start the new service against the isolated database. Migrations run transactionally under a PostgreSQL advisory lock. Digest mismatches abort startup. Check /readyz, scoped records, device revocation and integration intents before switching traffic. Roll back using a compatible service binary and the saved database. Reconcile provider side effects that occurred since the backup.
4. Stop the sync service before replacing its executable. Back up the runner's private application data directory, repository metadata and owned worktrees together while all local processes are quiescent. Restoring a journal alone cannot restore uncommitted files. Test restored devices against an isolated workspace.

## Retention and diagnostics

The service checks retention hourly. The default content window is 30 days per project. POST /api/v1/projects/{id}/retention accepts days and the reviewed project version. Expired shared diffs are removed. Expired conversation bodies and processed incident descriptions, steps and frames are redacted. Occurrence metadata, release/environment observations, deduplication keys, audit entries and deployment receipts remain for reconciliation. A previously queued incident with expired evidence is blocked before publication. Reopening a fresh observation can supply new evidence. Expired pairing grants and login sessions are cleaned up. Local worktrees and GitHub content have separate lifecycles. Purge database backups separately in line with the owner's retention requirements.

The runner stores local evidence and project files under its normal-user data directory and retained Git worktrees. Inspect status, branch and dirty changes before explicit worktree cleanup. Output from doctor and structured server request logs excludes request bodies and model credentials. For failed startup, inspect PostgreSQL connectivity, schema permissions, applied migration checksums and the origin setting. For pairing 403, check the grant expiry, target workspace and revocation. For 409 on a decision or reservation, fetch the current subject and resolve the recorded blocker. A 426 calls for a matching runner protocol binary.

## Workspace controls and explicit diff sharing

Settings provides a workspace emergency stop. It pauses every project, rejects new admission and records stop requests for the owning devices. Requests for disconnected machines remain pending. Unlock the workspace and review each project's resume decision before admitting new work. Device-local code can continue until the runner observes cancellation or loses its short-lived execution authority. Inspect actual acknowledgments and local process state during recovery.

On a completed attempt, `bugsnitch share-diff --id ATTEMPT` previews the redacted candidate diff for the approved files. To share that exact text, repeat the command with `--approve SHA256_FROM_PREVIEW`. The service verifies the enrollment, digest, content size, current subject and redaction. The dashboard can display that revision-bound artifact until its retention period expires. Only share content the owner is authorized to disclose.

Supervised is the initial per-project preset. Explicit local selection, planned issue publication, owner incident promotion and reviewed draft PR requests are permitted. Assisted requires an approved plan and disables external issue and draft writes by default. Autonomous within limits additionally enables incident promotion when three distinct recent Sentry events occur in a shared known environment and release within 24 hours. Individual action overrides are available. Automatic production deployment and merge remain unavailable. Verify CI deployment behavior on the target branch before a human merge.

For a consequential action, inspect the workspace event in the authenticated state API. A decision answer carries the selected option and authorized subject version. An accepted Sentry observation records the verified installation. Its subsequent grouping has a separate service event linked to the inbox row. Policy-gated publication records bind to the approved policy version. Audit metadata shares the transaction with the underlying state change. Migration 017 leaves existing event attribution unknown. [API fields](../api/README.md)

## Workspace diagnostics

Sign in and open `/api/v1/diagnostics` on the service origin. The owner-only JSON response takes one consistent database snapshot. It includes the following measurements.

| Field | Measurement |
| --- | --- |
| `observedAt`, `queues` | Pending count and age in milliseconds of the oldest pending item for incident intake, incident issue creation, planned issue creation, draft PRs and unexpired attempt controls. Counts include reconciliation and backoff. A queue with more than 1000 pending items returns 1000 and `more: true`. An empty queue has a null age. |
| `observations.attemptReasons` | Counts grouped by execution state and recorded reason code across the most recent 200 attempt reports. A failed historical report can have a null reason. |
| `observations.stageActivity` | Count, measured count, mean and maximum milliseconds for each reported phase in that same sample. A worker measures a model-stage call from local adapter invocation through return, including runtime startup. It times the validation command separately. Waiting for the owner and time between phases are excluded. Only recorded stage results contribute. A null mean or maximum means there were no measurements for that phase. |
| `observations.generalPermitHold` | Hosted elapsed milliseconds between acquisition and release for the 200 most recent completed planner, manager and proof permits. These intervals can include acknowledgement or reconnect delays. |
| `observations.contact` | First tracked sync, tracked and total device count, and up to 1000 observed reconnects in the preceding 24 hours. A successful authenticated sync after a measured gap longer than two minutes records one reconnect. The first sync after upgrade establishes tracking. The count is null until a device has synced. Sleep, connectivity loss and a stopped runner can all create a gap. |

The endpoint returns aggregates without device names, repository names, message text, tokens or command output. New runners send stage durations only when the server advertises timing support. Historical and older-runner stage results retain unknown durations. Subscription usage and cost require separate runtime-provided data.
