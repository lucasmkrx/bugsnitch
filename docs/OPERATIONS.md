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

### Provision a hosted pilot workspace

Provision one Compose project per pilot customer. Give each project a unique name, HTTPS hostname and loopback `BUGSNITCH_HOST_PORT`. Keep a separate environment file with its own owner credential, database password and optional MCP token. Use unique provider credentials and configure callbacks for that workspace’s hostname. Compose scopes its database volume and network to the project name. Point each hostname at its own loopback port in the reverse proxy. Separate hostnames keep browser session cookies scoped to one workspace.

For an instance named `pilot-acme`, save the values in a private environment file. Set `BUGSNITCH_ORIGIN=https://acme.example.com` and `BUGSNITCH_HOST_PORT=8788` in that file, then start the instance.

```sh
docker compose --project-name pilot-acme --env-file /path/to/private/pilot-acme.env \
  -f deploy/compose.yml up --build --detach
```

Set up HTTPS for that hostname and check `/readyz` through the proxy. Sign in, create a project, pair a runner and verify that it syncs. With two instances running, check that each owner credential and browser session is rejected by the other instance. Record the application revision and back up the instance database before inviting the owner. Reuse the Compose project name on updates to keep the existing volume. Keep customer onboarding and billing operator-managed during the pilot.

## Install the macOS runner

Follow the [macOS build instructions](../README.md#verification-and-operations) or obtain the archive for your CPU. Extract it into a private directory and run `./bugsnitch doctor`, then `./runner-service install "$PWD/bugsnitch"`. The installer copies both matched executables into a versioned directory and updates the launchd configuration. Starting the service uses that installed pair. For an update, stop the service, install the new pair and start it. Retain the previous archive until the new version has synchronized. To roll back this stage, stop, reinstall the previous release and start. Remove unused releases from the runner’s service directory after confirming the update. Pair and register under the same normal user. Run `scripts/runner-service start` to run the sync loop at login, `scripts/runner-service status` to inspect launchd and `scripts/runner-service stop` to unload it. Service installation is explicit. Local execution still needs the approved repository, Git, the supported Codex binary and the project's validation toolchain. Closing the ChatGPT window does not itself terminate the separate CLI process. Sleep and network loss suspend contact.

## Update, backup and restore

Upgrade the hosted service before updating runners. The current service accepts older runners. A current runner recovering an unacknowledged turn start needs a service that understands `turn_abandon`. It keeps the recovery request in its journal until the service acknowledges it. For an older runner’s waiting attempt that lost a local ticket slot before admission, inspect the attempt and use `local-continue` on its original device when capacity is available.

1. Stop new dispatch, reconcile active local work and record the deployed software revision. Take a PostgreSQL custom-format backup with `pg_dump -Fc --no-owner -f bugsnitch.dump "$DATABASE_URL"`. Protect the dump as sensitive workspace data.
2. Restore into an empty, isolated database with `pg_restore --exit-on-error --no-owner --dbname="$RESTORE_DATABASE_URL" bugsnitch.dump`. Disable provider credentials while verifying the restored system, to avoid running two writers for the same installation.
3. Start the new service against the isolated database. Install all four executables from `make server` together with the platform libpq runtime, or use the packaged container. At startup the domain worker runs the matching PostgreSQL migrator with a bounded deadline. Migrations run transactionally under a PostgreSQL advisory lock. Digest mismatches abort startup. Check /readyz, scoped records, device revocation and integration intents before switching traffic. Roll back using a compatible service binary and the saved database. Reconcile provider side effects that occurred since the backup.
4. Stop the sync service before replacing its executable. Back up the runner's private application data directory, repository metadata and owned worktrees together while all local processes are quiescent. Restoring a journal alone cannot restore uncommitted files. Test restored devices against an isolated workspace.

## Retention and diagnostics

The service checks retention hourly. The default content window is 30 days per project. POST /api/v1/projects/{id}/retention accepts days and the reviewed project version. Expired shared diffs are removed. Expired conversation bodies and processed incident descriptions, steps and frames are redacted. Occurrence metadata, release/environment observations, deduplication keys, audit entries and deployment receipts remain for reconciliation. A previously queued incident with expired evidence is blocked before publication. Reopening a fresh observation can supply new evidence. Expired pairing grants and login sessions are cleaned up. Local worktrees and GitHub content have separate lifecycles. Purge database backups separately in line with the owner's retention requirements.

The runner stores local evidence and project files under its normal-user data directory and retained Git worktrees. Inspect status, branch and dirty changes before explicit worktree cleanup. Output from doctor and structured server request logs excludes request bodies and model credentials. For a missing server executable, reinstall the full server bundle. For a missing or incompatible runner helper, reinstall the matching runner pair and run doctor. A small version string with `capacity=1 execution=1 controls=1 attempts=1` confirms support for configured ticket capacity, native worktrees, validation and control receipts. Stop active work before rolling back executables. Restore the matching pair together and retain the SQLite journals and Git worktrees. Missing validator exit evidence requires inspection on the original device. For other startup failures, inspect PostgreSQL connectivity, schema permissions, applied migration checksums and the origin setting. For pairing 403, check the grant expiry, target workspace and revocation. For 409 on a decision or reservation, fetch the current subject and resolve the recorded blocker. A 426 calls for a matching runner protocol binary.

## Hosted coordination and recovery

Build with `make server`. Install `bugsnitch-server`, `bugsnitch-domain`, `bugsnitch-server-migrate` and `bugsnitch-provider` in one directory. Start only `bugsnitch-server`. The supported hosted deployment uses Linux or macOS with PostgreSQL 17 and libpq. `BUGSNITCH_LISTEN`, `BUGSNITCH_ORIGIN`, `DATABASE_URL` and the existing provider settings keep their established meanings. The operator's HTTPS proxy terminates public TLS.

The Rust listener starts a private Go domain worker in an owner-only temporary directory. If that worker exits, the listener exits and a service manager can restart the bundle. `/healthz` reports process liveness. `/readyz` checks the Rust coordination path and the domain worker's database connection. Budget up to 24 persistent PostgreSQL connections per service instance (15 from the Go pool, four each for public and background coordination, and one dedicated notification session), plus migration and operational headroom. Restore the matching four binaries together on rollback.

If a write times out or its reply is lost, inspect the record before acting again. The runner reuses its saved attempt sequence or control ID. Outbox claims become eligible again after their one-minute reservation expires. The consumer reconciles a potentially completed GitHub write using its existing identity. A missed heartbeat preserves ownership. To reproduce the process and database checks, build the binaries, set the disposable database variables described in the README and run `scripts/smoke-devices.py`, `scripts/smoke-server-lifecycle.py`, `scripts/smoke_inbound.py` and `scripts/smoke.py` with Python 3.

Migration 021 adds wakeup triggers to the work queues and their eligibility settings. The notification session uses a schema-specific channel with an empty payload. After subscribing or reconnecting, the service scans committed rows. One Rust transaction reserves at most one due item per available provider queue. Each consumer rechecks its lease and project authority before contacting GitHub. If a notification is lost or a retry becomes due during a quiet period, the service rescans every five seconds. Use a direct PostgreSQL connection or PgBouncer session pooling for immediate wakeups. PgBouncer transaction pooling lacks support for `LISTEN`. [PostgreSQL notification setup](https://www.postgresql.org/docs/17/sql-listen.html) · [PgBouncer feature map](https://www.pgbouncer.org/features.html)

Migration 020 backfills workspace event positions and creates an index while holding the events table lock. Schedule the upgrade around write traffic on large installations. Keep the counter table in backups with the event history. After restoring, read `/api/v1/events` using a cursor saved before the backup, then verify that a new event advances the cursor. [Replay protocol](../api/README.md#event-attribution)

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
