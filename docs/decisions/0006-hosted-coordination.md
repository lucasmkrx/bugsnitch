# 0006 · Rust owns hosted coordination transactions

Accepted 2026-11-22. The server must support previously paired runners while moving PostgreSQL coordination into Rust.

The Go HTTP and MCP entry points keep their public protocols. They send named operations over private, length-prefixed STDIO to the installed Rust server companion. Up to four child processes each own one libpq connection. This keeps the transaction, locks, outcome and audit write on one connection and permits concurrent claims without a new service to deploy. The Go caller supplies trusted actor context. Rust resolves device identity from the credential digest within the transaction. The database receives parameters separately from SQL. [PostgreSQL parameterized execution](https://www.postgresql.org/docs/17/libpq-exec.html)

The bridge never retries an operation after losing a response. The caller reconciles using the existing durable identity. Background jobs select a due row under a skip-locked query, reserve it for one minute and return a fence. The Go provider client rechecks the locked row, fence and authorization before acting. Its existing reconciliation rules cover an uncertain external write. [PostgreSQL row locking](https://www.postgresql.org/docs/17/sql-select.html)

PostgreSQL assigns sequence numbers before commit. Reading a page by that number could skip an older uncommitted event permanently. Migration 020 uses a deferred PostgreSQL trigger to assign replay positions at commit. All event writers, including older services, acquire the per-workspace counter lock after domain writes. The counter and event commit or roll back together. This serializes event publication within one workspace and allows independent workspaces to advance concurrently. The original sequence remains the event identity. [PostgreSQL sequence semantics](https://www.postgresql.org/docs/17/functions-sequence.html)

The Go public entry points and provider client describe stage 3. The next cutover moved the public listener, hosted MCP and GitHub App client to Rust. The private Go application worker retained its domain operations. [HTTP and provider cutover](0007-rust-http-and-providers.md)
