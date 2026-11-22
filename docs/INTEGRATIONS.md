# Integration inventory

See CODEX.md for local runtime protocol and authorization evidence, PROVIDERS.md for GitHub and Sentry, and DASHBOARD.md for frontend versions and browser checks.

The local Codex account, hosted owner, paired runner and provider installations have separate credentials. A returned model name does not prove a model turn. Shared subscription capacity is unknown until the runtime supplies it. An OpenAI access decision for the hosted service cannot be simulated by an operator setting.

## ChatGPT metadata tools

The Rust service uses the official rmcp SDK 3.11.0 to serve stateless Streamable HTTP at /mcp. The desktop bugsnitch-mcp executable uses the Go MCP SDK v1.8.0 to bridge those authorized tools into local STDIO. The public tool names and argument schemas are preserved. Rust queries PostgreSQL directly for project, attempt and decision lists. Single-subject reads and mutations use the private application worker. Available tools list projects, attempts and decisions, report a manual incident, and apply an owner's version-bound answer. Reads require workspace.read. Writes additionally require incident.write or decision.write. These calls share persisted workspace records. [Rust MCP SDK](https://docs.rs/rmcp/3.11.0/rmcp/) · [Streamable HTTP](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports)

### Desktop ZIP and bridge credentials

Build the native desktop package from the repository root. The default target matches the local Go platform. For another supported target, use `GOOS=darwin GOARCH=amd64 make plugin` or select `arm64`. Linux supports the same two architectures. Python 3 and Go are build prerequisites. The resulting ZIP includes the executable, portable manifests, usage skill, and licenses.

```sh
make plugin
archive="bin/plugins/bugsnitch-$(go env GOOS)-$(go env GOARCH).zip"
unzip -o "$archive" -d .local/plugin-smoke
.local/plugin-smoke/bin/bugsnitch-mcp configure --origin http://localhost:8787 < .local/dev/plugin-token
```

The final command saves that service origin and its scoped Bugsnitch token together. The local developer stack generates the token file. For a hosted service, use its HTTPS origin and supply its private plugin token through redirected standard input. The service validates the token’s scopes. Its private-token default grants workspace reads, manual incident writes, and decision answers. To rotate, replace the server token, restart the service, and run the configuration command with the new token.

The bridge stores `connection.json` with mode 0600 under a mode-0700 `bugsnitch-mcp` folder in the OS user configuration directory. On macOS this is `~/Library/Application Support/bugsnitch-mcp/`. On Linux it is `$XDG_CONFIG_HOME/bugsnitch-mcp/` or `~/.config/bugsnitch-mcp/`. Only configure it for the OS user who will run the plugin. Removing the local file clears the saved connection. Revoking the token on the service also invalidates existing copies.

In a compatible desktop host, install the ZIP through its local plugin flow. The root MCP manifest starts `${PLUGIN_ROOT}/bin/bugsnitch-mcp`. A standalone STDIO host can launch the extracted executable directly. Existing environment-based callers can supply `BUGSNITCH_ORIGIN` and `BUGSNITCH_MCP_TOKEN` together. For compatibility, a token alone uses `http://localhost:8787`. With neither variable set the bridge loads both values from its saved profile. A supplied origin alone fails before any credential is sent. Credentials stay outside the archive. [OpenAI portable plugin packaging](https://developers.openai.com/plugins/build/plugins) · [Plugin manifest schema 1.0.0](https://agent-plugins.org/schemas/1.0.0/plugin.schema.json) · [MCP manifest schema 1.0.0](https://agent-plugins.org/schemas/1.0.0/mcp.schema.json)

For the lifecycle smoke test, set the disposable PostgreSQL environment described in OPERATIONS.md. After extracting the native package, use `python3 scripts/smoke.py --mcp-bridge .local/plugin-smoke/bin/bugsnitch-mcp`. The smoke process provides its own temporary service credentials, lists a real persisted project through STDIO, and verifies a submitted report through the dashboard API. Host installation and any public directory submission require separate live verification.

### Hosted HTTP

For a hosted ChatGPT custom MCP server, configure an OAuth 2.1 provider supporting PKCE and resource-specific access tokens. Register the ChatGPT client and documented callback at the provider, configure explicit scopes and owner consent, then configure MCP_OAUTH_ISSUER, MCP_OAUTH_INTROSPECTION, MCP_OAUTH_CLIENT_ID, MCP_OAUTH_CLIENT_SECRET and MCP_OAUTH_OWNER_SUBJECT. The service requires active introspection, exact issuer and subject, audience equal to BUGSNITCH_ORIGIN/mcp, and a future exp. Discovery is at /.well-known/oauth-protected-resource/mcp. Test the flow with a development tenant and the actual host before granting production access. No vendor client or redirect registration has been performed in this repository. Desktop-only directory submissions have a separate partner approval process.

[OpenAI plugin authentication](https://developers.openai.com/plugins/build/auth) · [ChatGPT connection workflow](https://developers.openai.com/plugins/deploy/connect-chatgpt) · [Desktop-only submission](https://developers.openai.com/plugins/deploy/desktop-only) · [Official Go MCP SDK](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk@v1.8.0/mcp)

The MCP server also provides get_project_status and inspect_attempt. Optional scopes plan.write, feedback.write and attempt.control enable provisional ticket proposals, recorded project feedback and version-bound pause/stop requests. Set BUGSNITCH_MCP_SCOPES to an explicit space-separated grant for the private bridge if these extra tools are needed. Its default retains workspace.read, incident.write and decision.write. OAuth scopes always come from the validated token. Review publication separately in the dashboard. Hosted work never starts inference merely because a plugin proposed scope or supplied feedback.

The verified build uses Go 1.27.4, pgx v5.13.0, PostgreSQL 17.12, Node 26.13.1 and the versions pinned in web/package-lock.json. The native storage components use Rust 1.100.0. The runner bundles SQLite 3.54.1 via rusqlite 0.43.0. Hosted migrations and native coordination use the libpq 6.0.1 Rust crate with the platform PostgreSQL client library. Cargo.lock records transitive versions. GitHub REST requests pin 2026-03-10, and GraphQL reads validate the returned repository and revision. [Go release history](https://go.dev/doc/devel/release) · [pgx release](https://pkg.go.dev/github.com/jackc/pgx/v5@v5.13.0) · [Git worktrees](https://git-scm.com/docs/git-worktree)
