# Bugsnitch

Bugsnitch helps an individual developer take a bug from report to verified fix. Triage the report in the dashboard, approve work on a GitHub issue, and run the repair in a separate Git worktree on your Mac. The runner validates the candidate and records review evidence. Bugsnitch tracks verification against the specific deployed build.

**Developer preview.** Run Bugsnitch from source with one owner per workspace. Managed hosting is available through invitation-only pilots. The [v0.2.0 release](https://github.com/lucasmkrx/bugsnitch/releases/tag/v0.2.0) is for the earlier VS Code extension.

The macOS runner keeps worktrees and detailed execution evidence on your machine. It runs the approved validation command with your local user's permissions. The server receives status and bounded metadata. You can approve sharing a specific candidate diff. For live repairs, Codex receives the selected task context and handles its own local sign-in. [Architecture](docs/ARCHITECTURE.md) · [Local model authorization](docs/CODEX.md)

## Try it locally

Install Git, Go (tested with 1.27.4), rustup, Node 26.13.1 with npm, PostgreSQL 17 with libpq development headers, libclang, a C compiler and OpenSSL. Cargo selects the pinned Rust 1.100.0 toolchain. Put Cargo and `pg_config` on `PATH`. On macOS with Homebrew and Xcode Command Line Tools, run

```sh
export PATH="$(brew --prefix rustup)/bin:$(brew --prefix postgresql@17)/bin:$PATH"
```

Clone the repository and run the following commands from its root.

```sh
git clone https://github.com/lucasmkrx/bugsnitch.git
cd bugsnitch
```

### Try the bundled checkout repair

Install Chrome or set `BUGSNITCH_TEST_CHROME` to your Chromium executable, then run

```sh
make checkout-acceptance
```

The exercise reproduces a checkout bug in Chromium, promotes the incident to a fixture GitHub issue, applies a scripted repair in a temporary worktree and verifies the deployed result in the browser. It starts a disposable database by default and runs without GitHub, Sentry or ChatGPT accounts. [Checkout walkthrough](examples/checkout/README.md) · [Verification ledger](docs/PROGRESS.md)

## Start the dashboard

```sh
make dev
```

Open **http://localhost:8787**. Sign in using the high-entropy credential saved in **.local/dev/owner-token**. The script creates a private Unix-socket PostgreSQL cluster, applies migrations, installs the locked web dependencies, builds the dashboard and starts the service. Stop the foreground service with Ctrl-C. Use `scripts/dev stop` to stop its database. The next start reuses the data. To choose a PostgreSQL installation, set PGBIN to its bin directory.

You can also [self-host with Docker Compose](docs/OPERATIONS.md#hosted-service). [Dashboard walkthrough and screenshots](docs/DASHBOARD.md)

### Connect a macOS runner

In Settings, register a project by its GitHub owner/repository name. In Devices, issue a ten-minute pairing code for the specific computer and projects. Build and connect a runner on that computer.

```sh
make runner
bin/bugsnitch doctor
bin/bugsnitch pair --server http://localhost:8787 --code PAIRING_CODE
bin/bugsnitch register --project PROJECT_ID --path /absolute/path/to/checkout
bin/bugsnitch sync --watch
```

Use the public HTTPS service origin on another computer. Each project checkout needs a matching GitHub origin. Project execution configuration, selected issue revisions, validation and local worktree recovery are described in [the runner guide](docs/RUNNER.md). The local runtime requires separate supported ChatGPT sign-in. The service never receives that credential.

## Workflows

- Inspect projects, connection status and recent execution reports. Queue a manual report with observed behavior, expected behavior and reproduction steps. Approved incident promotion uses a scoped GitHub App integration.
- Propose and approve a versioned plan. Approved criteria, dependencies and shared resource declarations constrain local admission. Pausing closes the gate to new work. Resuming requires a decision bound to the current project version.
- Use `bugsnitch next --project PROJECT_ID` to inspect the next eligible issue from the approved plan. Run independent issues in separate worktrees with `bugsnitch local-run --project PROJECT_ID`. Each worker validates its candidate and collects separate review evidence. New runners default to two active tickets and two simultaneous model turns. Use `bugsnitch local-limits` to view or change these values on the Mac. **Settings → Workspace capacity** controls the shared workspace limits and each enrolled device's model-turn limit. [Parallel batch example](docs/RUNNER.md#execute-a-bounded-local-batch)
- Record deployment and validation evidence for the specific incident, environment and build. PR readiness, merging and deployment have separate evidence requirements.
- Use the local MCP bridge or an OAuth-configured hosted MCP connection to read project and decision state, record reports and answer current decisions. Dashboard messages persist with their inference availability status.
- For an issue whose code has already deployed, use `bugsnitch local-verify` to record a passed, failed or pending result with evidence for each criterion. It records the build and environment without creating a commit. [Verification walkthrough](docs/RUNNER.md#verify-an-issue-on-an-existing-deployment)

## Verification and operations

```sh
make test
make build
```

Database tests use a disposable instance with schema-creation privileges. Set BUGSNITCH_TEST_DSN and BUGSNITCH_TEST_DATABASE_URL to its PostgreSQL URI, then run `make test GO_TEST_FLAGS="-race -count=1"`. Run `cargo test --locked -p bugsnitch-server-migrate --test database -- --ignored` against the same disposable database for migration compatibility, concurrency and rollback. For focused Go tests that use either store, run `make helpers` and export the matching executable paths first.

```sh
export BUGSNITCH_JOURNAL_HELPER="$PWD/bin/bugsnitch-runner-journal"
export BUGSNITCH_MIGRATOR="$PWD/bin/bugsnitch-server-migrate"
export BUGSNITCH_PROVIDER="$PWD/bin/bugsnitch-provider"
export BUGSNITCH_TEST_SERVER="$PWD/bin/bugsnitch-server"
```

On macOS, install both compilation targets with `rustup target add aarch64-apple-darwin x86_64-apple-darwin` and run `make macos`. Each archive in `bin/packages/` contains the runner, its matching Rust executable and the service installer. For a complete installation exercise, build the binaries, set PGHOST, PGPORT and PGUSER for a disposable instance with CREATEDB permission, and run `python3 scripts/smoke.py`. The script creates two temporary databases, two runner profiles and a fixture checkout.

[Operations guide](docs/OPERATIONS.md) · [Provider configuration](docs/INTEGRATIONS.md) · [Architecture](docs/ARCHITECTURE.md) · [Implementation plan](docs/IMPLEMENTATION_PLAN.md) · [Product brief](docs/PRODUCT.md)

## Contributing and license

[Contributing guide](CONTRIBUTING.md) · [Support](SUPPORT.md) · [Private security reporting](SECURITY.md)

The source is licensed under the [Mozilla Public License 2.0](LICENSE). The [trademark terms](TRADEMARKS.md) apply to the Bugsnitch name and brand assets.
