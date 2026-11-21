# Operations

Run one hosted service behind HTTPS with PostgreSQL on a private network. Use an independent database for tests. The owner bootstrap secret is a high-entropy login credential. Rotate it and invalidate existing sessions if compromised. Revoke device credentials from the dashboard when a computer is retired.

Keep database backups encrypted. Restore a logical backup into a fresh database and verify workspace, project, incident and event counts before switching traffic. Local worktrees and SQLite journals require separate device backups. Database backups do not contain uncommitted work.

## Hosted service

The native startup is documented in README.md. The container alternative uses `cp deploy/example.env .env`, three independent hex secrets as needed, then `docker compose --env-file .env -f deploy/compose.yml up --build`. The service binds to loopback; PostgreSQL stays on the Compose network. Use a private link with certificate verification for external PostgreSQL. Set BUGSNITCH_ORIGIN to the public HTTPS origin, including the correct port where applicable. BUGSNITCH_LISTEN controls the HTTP listener. Set BUGSNITCH_WEB_DIR for an alternate web/dist location.

For bugsnitch.com, provision the database and host, route the domain to that host after owner approval and terminate TLS with the supplied Caddyfile or an equivalent HTTPS proxy. Set proxy timeouts and request limits appropriate to webhook size. Use a secret manager for DATABASE_URL, BUGSNITCH_OWNER_TOKEN, GitHub App and Sentry secrets. Inject GITHUB_APP_CLIENT_ID, GITHUB_APP_KEY_FILE, GITHUB_WEBHOOK_SECRET and SENTRY_CLIENT_SECRET only on the server. Mount the PEM read-only for the service user. Provider mappings and callback setup are described in PROVIDERS.md. No DNS or production changes were performed.

The hosted bootstrap credential belongs to the owner. Browser sessions use hashed opaque secrets with a 12-hour expiry. Cookies use HttpOnly, SameSite Strict and Secure on HTTPS. Changing BUGSNITCH_OWNER_TOKEN affects future sign-in; deleting this owner's sessions forces sign-in again. Connections from devices use a separate 256-bit local credential, with only its digest stored centrally. Rotate a device by revoking it in Devices, removing the local enrollment with unpair, then pairing it again. Revocation blocks subsequent sync and admission. A sleeping computer may retain uncommitted work. Confirm quiescence on that computer before recovering reservations.

## Install the macOS runner

Run `make macos` to build arm64 and amd64 binaries. Select the matching binary and run `scripts/runner-service install bin/bugsnitch-darwin-arm64` or the amd64 equivalent. Pair and register under the same normal user. Run `scripts/runner-service start` to run the sync loop at login, `scripts/runner-service status` to inspect launchd and `scripts/runner-service stop` to unload it. Service installation is explicit. Local execution still needs the approved repository, Git, the SQLite command-line program, the supported Codex binary and the project's validation toolchain. Closing the ChatGPT window does not itself terminate the separate CLI process. Sleep and network loss suspend contact.

## Update, backup and restore

1. Stop new dispatch, reconcile active local work and record the deployed software revision. Take a PostgreSQL custom-format backup with `pg_dump -Fc --no-owner -f bugsnitch.dump "$DATABASE_URL"`. Protect the dump as sensitive workspace data.
2. Restore into an empty, isolated database with `pg_restore --exit-on-error --no-owner --dbname="$RESTORE_DATABASE_URL" bugsnitch.dump`. Disable provider credentials while verifying the restored system, to avoid running two writers for the same installation.
3. Start the new service against the isolated database. Migrations run transactionally under a PostgreSQL advisory lock. Digest mismatches abort startup. Check /readyz, scoped records, device revocation and integration intents before switching traffic. Roll back using a compatible service binary and the saved database. Reconcile provider side effects that occurred since the backup.
4. Stop the sync service before replacing its executable. Back up the runner's private application data directory, repository metadata and owned worktrees together while all local processes are quiescent. Restoring a journal alone cannot restore uncommitted files. Test restored devices against an isolated workspace.

## Retention and diagnostics

Hosted records persist until owner/operator deletion. Admission receipts, decision versions, canonical mappings, inbox deduplication keys and outbox markers participate in correctness. Keep them with their associated workspace during backups. Use a separate workspace database for deletion testing. Removing an entire workspace installation requires decommissioning the server, devices, integration credentials and its database/backups. Automatic TTL policies for selectively expiring incident content and conversation history are outstanding. Expired owner sessions and pairing grants cease authorization at query time.

The runner stores local evidence and project files under its normal-user data directory and retained Git worktrees. Inspect status, branch and dirty changes before explicit worktree cleanup. Output from doctor and structured server request logs excludes request bodies and model credentials. For failed startup, inspect PostgreSQL connectivity, schema permissions, applied migration checksums and the origin setting. For pairing 403, check the grant expiry, target workspace and revocation. For 409 on a decision or reservation, fetch the current subject and resolve the recorded blocker. A 426 calls for a matching runner protocol binary.
