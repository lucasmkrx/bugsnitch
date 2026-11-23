# Bugsnitch

Bugsnitch coordinates development across your GitHub projects and your own computers. The hosted service keeps project state in PostgreSQL. The public HTTP server, dashboard queries, device coordination, provider job selection, GitHub App client, webhooks and hosted MCP transport run in Rust. A private Go worker handles planning, decisions and background jobs. On the runner, Rust manages worktrees, SQLite and validation, and Go runs the CLI and model adapter. A macOS runner executes locally initiated work in a separate Git worktree, stores its evidence in SQLite, and reports bounded metadata to a React dashboard. An authenticated MCP server exposes the same workspace to compatible clients.

This rebuild is in development. The [verification ledger](docs/PROGRESS.md) records which workflows have run, their limits, and the outstanding live tests.

## Start the dashboard

Install Git, Go (tested with 1.27.4), rustup, Node 26.13.1 with npm, PostgreSQL 17 with libpq development headers, libclang, a C compiler and OpenSSL. Cargo selects the pinned Rust 1.100.0 toolchain. Put Cargo and `pg_config` on PATH. On macOS with Homebrew and Xcode Command Line Tools, run

```sh
export PATH="$(brew --prefix rustup)/bin:$(brew --prefix postgresql@17)/bin:$PATH"
```

Start the dashboard from the repository root with

```sh
make dev
```

Open **http://localhost:8787**. Sign in using the high-entropy credential saved in **.local/dev/owner-token**. The script creates a private Unix-socket PostgreSQL cluster, applies migrations, installs the locked web dependencies, builds the dashboard and starts the service. Stop the foreground service with Ctrl-C. Use `scripts/dev stop` to stop its database. The next start reuses the data. To choose a PostgreSQL installation, set PGBIN to its bin directory.

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
- Use `bugsnitch next --project PROJECT_ID` to inspect the next eligible issue from the approved plan. Start a bounded local pass with `bugsnitch local-run --project PROJECT_ID --limit 2 --parallel 2`. Each worker validates its own candidate and collects separate review evidence. Owner questions pause the affected work. Enrolled computers share ticket and model-turn limits. Initial limits are two active tickets per workspace and two model turns per workspace and per device.
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

Bugsnitch retains the [Mozilla Public License 2.0](LICENSE). The existing [trademark terms](TRADEMARKS.md) apply to its name and brand assets.
