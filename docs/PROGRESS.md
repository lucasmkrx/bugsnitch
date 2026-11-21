# Rebuild checkpoint

Started 2026-11-21 from a clean main at 833637906ffc7d7de633b0de64e58c0636cd3bfd in lucasmkrx/bugsnitch. The previous product was a TypeScript VS Code extension. Branch rebuild/bugsnitch-v2 preserves its history, MPL-2.0 license and brand terms.

Implementation in progress. External prerequisites include a deliberately selected GitHub App test installation, Sentry test installation, and confirmation of eligibility for remotely initiated inference. No production deployment or DNS changes have been requested.

## Verified in this checkout

- A real local ChatGPT app-server turn completed with Codex 0.171.0-alpha.13 and gpt-6.2-sol. The adapter confirmed ChatGPT sign-in and verified effective read-only thread restrictions before sending model input.
- A live local worker on a deliberately temporary elapsed-time bug fixture planned, implemented, validated and reviewed candidate 6f885a997348adc8c2104474704b171b5399ffbb. A separate model thread performed the review. The issue input was synthetic. No remote PR was created by that run.
- Two independent runner processes paired, mapped a temporary checkout and synchronized. The real local MCP STDIO bridge read a dashboard project and reported an incident that appeared through the browser API. A server restart preserved sessions and enrollments. pg_dump/pg_restore into a new database preserved the records and subsequent device revocation denied further sync.
- PostgreSQL integration tests and race-enabled Go tests passed for competing admission, retained model/issue reservations, resource conflicts, dependencies, stale decision answers, issue outbox reconciliation, webhook data handling, environment verification and PR evidence invalidation. The external provider HTTP responses in these tests are fixtures.
- Both macOS architectures cross-compiled; execution here used one Apple Silicon host. The dashboard production build passed.
