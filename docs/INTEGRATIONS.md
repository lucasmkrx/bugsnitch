# Integration inventory

See CODEX.md for local runtime protocol and authorization evidence, PROVIDERS.md for GitHub and Sentry, and DASHBOARD.md for frontend versions and browser checks.

The local Codex account, hosted owner, paired runner and provider installations have separate credentials. A returned model name does not prove a model turn. Shared subscription capacity is unknown until the runtime supplies it. An OpenAI access decision for the hosted service cannot be simulated by an operator setting.

## ChatGPT metadata tools

The Go MCP SDK is pinned to v1.8.0. The service exposes stateless Streamable HTTP at /mcp and the bugsnitch-mcp executable bridges those authorized tools into local STDIO. Available tools list projects, attempts and decisions, report a manual incident, and apply an owner's version-bound answer. Reads require workspace.read. Writes additionally require incident.write or decision.write. These calls share persisted workspace records. They never initiate inference on a paired device.

For a private local integration, generate a separate random BUGSNITCH_MCP_TOKEN (32 or more characters) on the service and pass it to the desktop bridge alongside BUGSNITCH_ORIGIN. The development script creates .local/dev/plugin-token. The private token carries all three tool scopes. Changing it and restarting the service revokes it. Install bin/bugsnitch-mcp on the host's PATH. Use integrations/chatgpt as the local plugin package or register the executable in a compatible STDIO MCP host.

For a hosted ChatGPT custom MCP server, configure an OAuth 2.1 provider supporting PKCE and resource-specific access tokens. Register the ChatGPT client and documented callback at the provider, configure explicit scopes and owner consent, then configure MCP_OAUTH_ISSUER, MCP_OAUTH_INTROSPECTION, MCP_OAUTH_CLIENT_ID, MCP_OAUTH_CLIENT_SECRET and MCP_OAUTH_OWNER_SUBJECT. The service requires active introspection, exact issuer and subject, audience equal to BUGSNITCH_ORIGIN/mcp, and a future exp. Discovery is at /.well-known/oauth-protected-resource/mcp. Test the flow with a development tenant and the actual host before granting production access. No vendor client or redirect registration has been performed in this repository. Desktop-only directory submissions have a separate partner approval process.

[OpenAI plugin authentication](https://developers.openai.com/plugins/build/auth) · [ChatGPT connection workflow](https://developers.openai.com/plugins/deploy/connect-chatgpt) · [Desktop-only submission](https://developers.openai.com/plugins/deploy/desktop-only) · [Official Go MCP SDK](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk@v1.8.0/mcp)
