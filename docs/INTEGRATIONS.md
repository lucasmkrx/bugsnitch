# Integration inventory

Connect the services your team uses for each delivery step. GitHub supplies the canonical issues and pull requests. Sentry can supply incident observations. Linear links an existing issue and can delegate it to an agent. Slack and Telegram let named approvers answer dispatch decisions. The ChatGPT bridge reads workspace records and offers writes according to its granted scopes. Each connection has its own credential. [GitHub and Sentry setup](PROVIDERS.md) · [Runner model access](CODEX.md) · [Workspace walkthrough](DASHBOARD.md)

## Linear issue links

Set `BUGSNITCH_LINEAR_API_KEY` on the server to a Linear API key with read access to the intended teams. In **Work → Linear issues**, select a linked GitHub issue and enter a Linear identifier or issue UUID. The service reads the issue title and status, records when it checked them and links to the source. Use **Refresh Linear issue** for an updated snapshot. GitHub remains the source of issue identity and approval for runner admission. [Linear GraphQL API](https://linear.app/developers/graphql)

### Delegate a linked issue

Configure the agent's Linear OAuth access token in `BUGSNITCH_LINEAR_AGENT_TOKEN`, signed webhook secret in `LINEAR_WEBHOOK_SECRET`, and `/hooks/linear` as the application's AgentSessionEvent URL. Install the app under its own identity with delegation access. Refresh its OAuth credential when it expires. Delegate or mention it on a linked issue. A signed, recent event registers a session against that exact issue. Equally ranked eligible plan tickets with active delegations are selected first. Existing plan, dependency, device and execution limits apply.

The agent posts progress to its Linear session, then posts the environment, build, merge revision and evidence digest from the deployment handover. A prompt requests the latest status. Failed delivery retries use the same activity ID and reconcile its session and body after an uncertain result. Automatic publication uses the separate agent token. The read-only issue key continues to work independently. [Linear agent sessions](https://linear.app/developers/agent-interaction) · [Webhook signatures and timestamps](https://linear.app/developers/webhooks)

## Named decisions in Slack and Telegram

Create a Slack slash command named `/bugsnitch` with request URL `https://YOUR-SERVICE/hooks/slack`. Configure `SLACK_SIGNING_SECRET` on the service. For Telegram, register the bot webhook at `/hooks/telegram` using `setWebhook` with a random `secret_token` matching `TELEGRAM_WEBHOOK_SECRET`. The bot handles private chats. In Settings → Workspace access, bind the provider's numeric Telegram user ID or Slack team and user IDs to an active named approver. Verify the account IDs with the member before linking.

Use `/bugsnitch list` to read pending dispatch decisions and `/bugsnitch resume ID VERSION` or `/bugsnitch pause ID VERSION` to answer. Each answer uses the same project scope, expiry, current revision and named audit actor as a browser answer. Revoke the member or unlink the identity to withdraw chat access. [Slack request verification](https://docs.slack.dev/authentication/verifying-requests-from-slack/) · [Telegram webhook setup](https://core.telegram.org/bots/api#setwebhook)

## Ordered releases and workspace budget

In **Settings → Multi-repository releases**, create a release from freshly inspected open PRs in at least two projects. Add stages in deployment order and select one environment. Each stage records its exact PR head and target. The protected queue holds subsequent stages until the earlier merged PR has a handover in that environment. An uncleared deployment hold on an earlier project in the same environment blocks downstream stages even after a handover. A changed head or target requires a fresh plan. Cancelling an order removes its dependency holds. Handover evidence remains visible on the PR.

**Settings → Monthly workspace turn budget** covers model starts on paired runners, including worker, manager, planner and proof turns. Duplicate authorizations consume one turn. The monthly counter uses UTC, including failed attempts that acquired a turn. Zero blocks new starts and a blank limit removes the ceiling. Standalone unpaired execution has its own local authority.

## ChatGPT metadata tools

Connect a compatible desktop host through the packaged STDIO bridge, or use the hosted OAuth endpoint. Both connect to the same workspace. Every request requires `workspace.read`. The server advertises the tools available to the authenticated grant.

| Task | Tools | Additional scope |
| --- | --- | --- |
| Look up projects, attempts and decisions | `list_projects`, `get_project_status`, `list_attempts`, `inspect_attempt`, `list_decisions` | None |
| Record a manual incident | `report_incident` | `incident.write` |
| Answer a pending decision at its current version | `answer_decision` | `decision.write` |
| Stage a plan with provisional issue numbers | `propose_ticket_plan` | `plan.write` |
| Record a project message | `submit_feedback` | `feedback.write` |
| Request pause or stop for an attempt | `request_attempt_control` | `attempt.control` |

The default private bridge grant includes `workspace.read incident.write decision.write`. To enable another tool, set `BUGSNITCH_MCP_SCOPES` on the service to the complete space-separated grant. OAuth uses the scopes on the validated token. The owner approves external issue publication in **Plans** and the resulting canonical execution plan has its own approval. The runner acknowledges a control request after it receives it. Submitting project feedback records a message for the workspace.

The Rust service serves stateless Streamable HTTP at `/mcp` using rmcp 3.11.0. The desktop `bugsnitch-mcp` bridge uses the Go MCP SDK v1.8.0. Rust reads the project, attempt and decision lists from PostgreSQL. Single-subject reads and mutations use the private application worker. [Rust MCP SDK](https://docs.rs/rmcp/3.11.0/rmcp/) · [Streamable HTTP](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports)

### Desktop ZIP and bridge credentials

Build the native desktop package from the repository root. The default target matches the local Go platform. For another supported target, use `GOOS=darwin GOARCH=amd64 make plugin` or select `arm64`. Linux supports the same two architectures. Python 3 and Go are build prerequisites. The resulting ZIP includes the executable, portable manifests, usage skill, and licenses.

```sh
make plugin
archive="bin/plugins/bugsnitch-$(go env GOOS)-$(go env GOARCH).zip"
unzip -o "$archive" -d .local/plugin-smoke
.local/plugin-smoke/bin/bugsnitch-mcp configure --origin http://localhost:8787 < .local/dev/plugin-token
```

The final command saves that service origin and its scoped Bugsnitch token together. The server reads its private bridge token from `BUGSNITCH_MCP_TOKEN`. The local developer stack generates the token file. For a hosted service, use its HTTPS origin and supply its private plugin token through redirected standard input. The service validates the token’s scopes. To rotate, replace the server token, restart the service, and run the configuration command with the new token.

The bridge stores `connection.json` with mode 0600 under a mode-0700 `bugsnitch-mcp` folder in the OS user configuration directory. On macOS this is `~/Library/Application Support/bugsnitch-mcp/`. On Linux it is `$XDG_CONFIG_HOME/bugsnitch-mcp/` or `~/.config/bugsnitch-mcp/`. Only configure it for the OS user who will run the plugin. Removing the local file clears the saved connection. Revoking the token on the service also invalidates existing copies.

In a compatible desktop host, install the ZIP through its local plugin flow. The root MCP manifest starts `${PLUGIN_ROOT}/bin/bugsnitch-mcp`. A standalone STDIO host can launch the extracted executable directly. Existing environment-based callers can supply `BUGSNITCH_ORIGIN` and `BUGSNITCH_MCP_TOKEN` together. For compatibility, a token alone uses `http://localhost:8787`. With neither variable set the bridge loads both values from its saved profile. A supplied origin alone fails before any credential is sent. Credentials stay outside the archive. [OpenAI portable plugin packaging](https://developers.openai.com/plugins/build/plugins) · [Plugin manifest schema 1.0.0](https://agent-plugins.org/schemas/1.0.0/plugin.schema.json) · [MCP manifest schema 1.0.0](https://agent-plugins.org/schemas/1.0.0/mcp.schema.json)

For the lifecycle smoke test, set the disposable PostgreSQL environment described in the [operations guide](OPERATIONS.md). After extracting the native package, use `python3 scripts/smoke.py --mcp-bridge .local/plugin-smoke/bin/bugsnitch-mcp`. The smoke process provides its own temporary service credentials, lists a real persisted project through STDIO, and verifies a submitted report through the dashboard API. Host installation and any public directory submission require separate live verification.

### Hosted HTTP

For a hosted ChatGPT custom MCP server, configure an OAuth 2.1 provider supporting PKCE and resource-specific access tokens. Register the ChatGPT client and documented callback at the provider, configure explicit scopes and owner consent, then configure MCP_OAUTH_ISSUER, MCP_OAUTH_INTROSPECTION, MCP_OAUTH_CLIENT_ID, MCP_OAUTH_CLIENT_SECRET and MCP_OAUTH_OWNER_SUBJECT. The service requires active introspection, exact issuer and subject, audience equal to BUGSNITCH_ORIGIN/mcp, and a future exp. Discovery is at /.well-known/oauth-protected-resource/mcp. Test the flow with a development tenant and the actual host before granting production access. No vendor client or redirect registration has been performed in this repository. Desktop-only directory submissions have a separate partner approval process.

[OpenAI plugin authentication](https://developers.openai.com/plugins/build/auth) · [ChatGPT connection workflow](https://developers.openai.com/plugins/deploy/connect-chatgpt) · [Desktop-only submission](https://developers.openai.com/plugins/deploy/desktop-only) · [Official Go MCP SDK](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk@v1.8.0/mcp)

The verified build uses Go 1.27.4, pgx v5.13.0, PostgreSQL 17.12, Node 26.13.1 and the versions pinned in web/package-lock.json. The native storage components use Rust 1.100.0. The runner bundles SQLite 3.54.1 via rusqlite 0.43.0. Hosted migrations and native coordination use the libpq 6.0.1 Rust crate with the platform PostgreSQL client library. Cargo.lock records transitive versions. GitHub REST requests pin 2026-03-10, and GraphQL reads validate the returned repository and revision. [Go release history](https://go.dev/doc/devel/release) · [pgx release](https://pkg.go.dev/github.com/jackc/pgx/v5@v5.13.0) · [Git worktrees](https://git-scm.com/docs/git-worktree)

## Device token usage and cost

In **Settings → Model usage and cost**, read 1–365 days of workspace or project usage. The service counts each admitted worker, planner, proof and manager turn once, including turns on failed and cancelled attempts. Paired devices upload provider-reported input/output token counts after the server advertises metering in its sync handshake. A durable local outbox retries interrupted deliveries. The server binds each measurement to the exact device and turn-start event. Historical turns, interrupted turns with missing final counts, and older devices appear in the unmetered count. Standalone runs have no hosted admission.

An owner can save USD input and output rates per million tokens for an exact model in the workspace report. Historical estimates use the price in effect at admission time. Changing or ending a price affects later turns. The optional report price form can instead value a model at supplied rates for the entire selected period. Estimates cover priced input and output tokens. Provider-specific discounts, cache pricing, infrastructure charges and subscription invoices have separate accounting. Project-scoped members can read usage for their assigned project. The owner API accepts rates at `POST /api/v1/usage/prices` and the report at `POST /api/v1/usage` or `POST /api/v1/projects/{project}/usage`.

An owner can read a workspace or project usage report, then set a **monthly cost threshold** for that scope. **Alert in activity** records one alert per scope, policy version and UTC month. **Alert and stop new turns** also rejects fresh paired admissions once measured, priced usage reaches the threshold. Worker and manager turns count for the workspace and their project. Planner and proof permits without a project count for the workspace. Already admitted turns can finish or reconcile. Late measurements use the original admission month and effective price. Unmetered turns and measured turns without a price contribute no amount to the subtotal and retain their separate counts. Parallel work can cross the threshold before measurements arrive, and subscription charges or provider-specific billing may differ. Keep the hard monthly turn cap when you need a strict admission count.

Both cost policies default to off. Read `monthlyThreshold` in the owner's usage report, then POST `{"version":0,"mode":"stop","thresholdUSD":25}` to `/api/v1/usage/threshold` or `/api/v1/projects/{project}/usage/threshold`. To disable a threshold, send its latest version, `"mode":"off"` and `"thresholdUSD":null`. Each scope has a separate policy version.

The Codex adapter reads version-matched usage notifications for its single-turn thread. Claude counts come from the completed Messages response when present. The telemetry contains an admission identifier, sequence, model and counts. Prompts, model output, provider turn IDs and credentials stay outside the metering request.

## Historical approval cases

Bugsnitch records head, target and base revisions during open PR inspections. In Settings, open a project's approval policy history and select a pre-merge revision. Record a confirmed outcome and its evidence digest. Assign calibration or holdout before tuning a policy. Optional review evidence must carry the exact revisions and its own SHA-256 reference. Download the labelled cases from this panel for the runner's `approval-preview` command. Reconstructing a case needs a local checkout containing those Git objects.

The export also includes confirmed current-build recurrences linked to the same incident issue, inspected merge revision and deployment monitor. These take precedence over a previous clean label. An incident observation with an ambiguous deployed revision does not label a PR. Missing historical review evidence yields a hold during replay. The export contains at most 500 distinct changes.
