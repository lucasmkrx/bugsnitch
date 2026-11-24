<p><img src="web/public/brand/app-icon-192.png" alt="Bugsnitch logo" width="80" height="80"></p>

# Bugsnitch

Bugsnitch is a software factory for teams that run coding agents on their own machines. Start with a GitHub issue and an approved plan. A registered runner makes the change in a Git worktree, validates the candidate, and sends it for independent review. Your team decides what to publish and can trace a verified fix back to the build its users received.

**Developer preview.** Build the factory from `main` or self-host it with Docker Compose. Hosted pilot access is by invitation. The numbered VS Code extension releases belong to the earlier product. [Self-hosting guide](docs/OPERATIONS.md#hosted-service) · [Changelog](CHANGELOG.md)

[Try the checkout demo](#try-it-locally) · [Open a local workspace](#start-the-dashboard) · [Follow an issue through release](#workflows) · [Documentation](#documentation)

## Try it locally

The bundled checkout advertises free shipping on orders of $50 or more, but charges $5 at exactly $50. The demo reproduces the bug in Chromium, opens a fixture issue, repairs it with a scripted worker, and tests the served checkout. It starts a disposable PostgreSQL instance and uses local provider fixtures. You can run it without a model or GitHub account.

Use Git, Make, Go (CI uses 1.27.4), rustup, Node.js 26.13.1 with npm, PostgreSQL 17 with its client development headers, libclang, a C compiler, OpenSSL, and Chrome or Chromium. Cargo selects Rust 1.100.0 from the repository. Put Cargo and `pg_config` on your `PATH`. On macOS with Homebrew and Xcode Command Line Tools, this selects the Homebrew tools.

```sh
export PATH="$(brew --prefix rustup)/bin:$(brew --prefix postgresql@17)/bin:$PATH"
```

```sh
git clone https://github.com/lucasmkrx/bugsnitch.git
cd bugsnitch
make checkout-acceptance
```

For a nonstandard Chromium installation, set `BUGSNITCH_TEST_CHROME` to the executable. The test verifies free shipping on the repaired build, requires a fresh check after redeployment, and returns the incident to triage when the bug recurs. [Checkout walkthrough](examples/checkout/README.md) · [Recorded results](docs/PROGRESS.md)

## Start the dashboard

Start the local service from the repository root.

```sh
make dev
```

Open the [local homepage](http://localhost:8787/) for the interactive release example or the [workspace](http://localhost:8787/app/) to sign in with the owner credential saved in `.local/dev/owner-token`. The script starts a private PostgreSQL cluster, applies migrations, and builds the web app. Stop the service with Ctrl-C and its database with `scripts/dev stop`. The next start reuses your data. For a read-only tour with sample data, the workspace demo needs only Node.js and npm. [Workspace walkthrough](docs/DASHBOARD.md) · [Read-only demo](docs/DASHBOARD.md#read-only-demo)

### Connect a macOS runner

Register a project in the workspace using its GitHub `owner/repository`. In **Devices**, issue a pairing code for the computer and assign its projects. On the runner, substitute that code, the assigned project ID and the path to a checkout with a matching GitHub origin. For a remote service, use its HTTPS origin.

```sh
make runner
bin/bugsnitch pair --server http://localhost:8787 --code PAIRING_CODE
bin/bugsnitch register --project PROJECT_ID --path /absolute/path/to/checkout
bin/bugsnitch sync --watch
```

Pairing connects the device. Before executing an issue, use `project-add` on that machine to register the permitted edit scope and validation command. Worktrees and detailed artifacts stay on the runner. The runner authenticates directly with the configured model provider and sends the context for that stage. For Codex-backed turns, install the pinned `codex-cli 0.171.0-alpha.13`, sign in locally, and run `bin/bugsnitch doctor` to check the runtime and native helper. Individual stages can use the Claude Messages API with a locally supplied API key. [Worker configuration](docs/RUNNER.md#select-a-project) · [Model access](docs/CODEX.md)

## Workflows

| Stage | What happens |
| --- | --- |
| Plan | Start with a canonical GitHub issue. Optionally link its Linear ticket. Approve the ticket's criteria, prerequisites and shared resources. Eligible independent tickets can run in parallel worktrees. [Runner guide](docs/RUNNER.md) |
| Review | The runner validates the candidate commit. A separate reviewer inspects its diff and the validation result. The **Work** board shows the next action when an attempt needs input. [Workspace guide](docs/DASHBOARD.md) |
| Ship | Approve the candidate and target. On the originating device, `local-ship` publishes the reviewed branch and requests a GitHub draft PR. Mark the PR ready for review on GitHub. Sign-off requires passing checks, GitHub approval, and independent review and combined-validation evidence for the inspected revision. Eligible repositories can opt into protected queue delivery. [PR workflow](docs/INTEGRATION_QUEUE.md) · [Approval policies](docs/APPROVAL_POLICY.md) |
| Verify | Record the merged revision, environment and build. For a monitored incident, a signed deployment event and an independent behavior check provide evidence for that deployment. A redeployment needs a fresh check. [Deployment monitors](docs/PROVIDERS.md#github-deployment-monitor) |

### Team delivery

Each workspace has one owner. Named operators can report incidents and inspect PRs. Approvers can act on plans, evidence and decisions within their assigned scope. Link a GitHub issue to Linear, answer dispatch decisions through a bound Slack or Telegram identity, or order release stages across repositories for one environment. [Team integrations](docs/INTEGRATIONS.md)

Use **Settings → Workspace capacity** to limit concurrent work and model turns. **Model usage and cost** shows reported usage and estimated spend. Owners can cap monthly turns and set cost thresholds. [Usage and budgets](docs/INTEGRATIONS.md#device-token-usage-and-cost)

## Documentation

| For | Start here |
| --- | --- |
| Using the workspace | [Product overview](docs/PRODUCT.md) · [Dashboard](docs/DASHBOARD.md) · [Local runner](docs/RUNNER.md) |
| Connecting services and tools | [GitHub and Sentry](docs/PROVIDERS.md) · [Linear, chat and MCP](docs/INTEGRATIONS.md) · [HTTP API](api/README.md) |
| Operating a deployment | [Operations and recovery](docs/OPERATIONS.md) · [Architecture](docs/ARCHITECTURE.md) · [Architecture decisions](docs/decisions/) |
| Evaluating the factory | [Factory comparison and controls](docs/FACTORY_EVAL.md) · [Issue trials](docs/TRIALS.md) · [Verification ledger](docs/PROGRESS.md) · [Implementation history](docs/IMPLEMENTATION_PLAN.md) |

## Verification and operations

Build and test from the repository root.

```sh
make test
make build
npm --prefix web run test:browser
```

For database-backed tests, set `BUGSNITCH_TEST_DSN` and `BUGSNITCH_TEST_DATABASE_URL` to a disposable PostgreSQL database whose user can create schemas. Then run the race-enabled tests and the migration suite.

```sh
make test GO_TEST_FLAGS="-race -count=1"
cargo test --locked -p bugsnitch-server-migrate --test database -- --ignored
```

For focused Go integration tests, run `make helpers server` first and export the paths to the packaged executables.

```sh
export BUGSNITCH_JOURNAL_HELPER="$PWD/bin/bugsnitch-runner-journal"
export BUGSNITCH_MIGRATOR="$PWD/bin/bugsnitch-server-migrate"
export BUGSNITCH_PROVIDER="$PWD/bin/bugsnitch-provider"
export BUGSNITCH_TEST_SERVER="$PWD/bin/bugsnitch-server"
```

To build the Intel and Apple Silicon runner archives on macOS, install both Rust targets and run the package build. The archives are written to `bin/packages/`.

```sh
rustup target add aarch64-apple-darwin x86_64-apple-darwin
make macos
```

For a packaged installation check, set `PGHOST`, `PGPORT` and `PGUSER` for a disposable PostgreSQL instance with permission to create databases, then run `python3 scripts/smoke.py` after `make build`. [Packaging, deployment and restore](docs/OPERATIONS.md)

## Contributing and license

Bug reports and feature proposals go through GitHub Issues. Report suspected vulnerabilities privately. [Contributing](CONTRIBUTING.md) · [Support](SUPPORT.md) · [Security reporting](SECURITY.md)

Bugsnitch source is licensed under the [Mozilla Public License 2.0](LICENSE). The name, logo and other brand assets have [separate terms](TRADEMARKS.md). [Official logo and wordmark](assets/brand/README.md)
