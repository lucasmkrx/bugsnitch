# Bugsnitch

Bugsnitch coordinates development across your GitHub projects and your own computers. The Go service keeps project state in PostgreSQL. A macOS runner executes locally initiated work in a separate Git worktree, stores its evidence in SQLite, and reports bounded metadata to a React dashboard. An authenticated MCP server exposes the same workspace to compatible clients.

This rebuild is in development. The [verification ledger](docs/PROGRESS.md) records which workflows have run, their limits, and the outstanding live tests.

## Start the dashboard

Install Git, Go 1.26 or newer, Node 24 with npm, PostgreSQL 17 command-line tools and OpenSSL. From a clean checkout run:

```sh
make dev
```

Open **http://localhost:8787**. Sign in using the high-entropy credential saved in **.local/dev/owner-token**. The script creates a private Unix-socket PostgreSQL cluster, applies migrations, installs the locked web dependencies, builds the dashboard and starts the service. Stop the foreground service with Ctrl-C. Use `scripts/dev stop` to stop its database. The next start reuses the data. To choose a PostgreSQL installation, set PGBIN to its bin directory.

In Settings, register a project by its GitHub owner/repository name. In Devices, issue a ten-minute pairing code for the specific computer and projects. Build and connect a runner on that computer:

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
- Run selected eligible work locally, validate its candidate and collect separate review evidence. Enrolled computers share ticket and model-turn limits. Initial limits are two active tickets per workspace and two model turns per workspace and per device.
- Record deployment and validation evidence for the specific incident, environment and build. PR readiness, merging and deployment have separate evidence requirements.
- Use the local MCP bridge or an OAuth-configured hosted MCP connection to read project and decision state, record reports and answer current decisions. Dashboard messages persist with their inference availability status.

## Verification and operations

```sh
go test ./...
make build
make macos
```

Database tests use a disposable instance with schema-creation privileges. Set BUGSNITCH_TEST_DSN and BUGSNITCH_TEST_DATABASE_URL to its PostgreSQL URI, then run `go test -race -count=1 ./...`. For a complete installation exercise, build the binaries, set PGHOST, PGPORT and PGUSER for a disposable instance with CREATEDB permission, and run `python3 scripts/smoke.py`. The script creates two temporary databases, two runner profiles and a fixture checkout.

[Operations guide](docs/OPERATIONS.md) · [Provider configuration](docs/INTEGRATIONS.md) · [Architecture](docs/ARCHITECTURE.md) · [Implementation plan](docs/IMPLEMENTATION_PLAN.md) · [Product brief](docs/PRODUCT.md)

Bugsnitch retains the [Mozilla Public License 2.0](LICENSE). The existing [trademark terms](TRADEMARKS.md) apply to its name and brand assets.
