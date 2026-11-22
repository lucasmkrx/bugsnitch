# 0007 · Rust serves HTTP and connects to providers

Accepted 2026-11-22. Previously installed runners, saved owner sessions, MCP clients and webhook subscriptions must continue to work when the public server changes language.

The installed public executable serves the existing routes using Axum and Hyper. It owns origin checks, peer-based login and pairing rate limits, session authentication, typed device operations, and the static dashboard. It verifies provider signatures on original bytes before passing sanitized values to the coordinator. The hosted MCP service uses the official Rust SDK and checks grant scopes before dispatch. [Axum](https://docs.rs/axum/0.8.9/axum/) · [Rust MCP SDK](https://github.com/modelcontextprotocol/rust-sdk)

Planning, decisions, dashboard projections and outbox scheduling keep their existing application rules in a private Go worker. A versioned, size-bounded Unix socket protocol carries named commands. The server creates a mode-0700 directory and the worker restricts the socket to mode 0600. Only trusted local processes running under the service identity should have its filesystem permissions. A pipe connects the worker lifetime to its parent. A worker exit stops the listener. A lost reply leaves the command outcome uncertain and never triggers an automatic IPC retry.

The Go worker asks the Rust GitHub executable for typed operations on one installation and repository. The Rust client owns RSA signing, token exchange, TLS, pagination and rate-limit handling. It checks identity before acting and uses the applicable read or write grant. PostgreSQL remains the source of authority for fencing, external-write intent and reconciliation. No public listener is exposed by either helper. [GitHub App installation authentication](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app)

The release keeps the existing API and database schema. Operators upgrade the four hosted binaries together. The runner and desktop bridge can be upgraded separately. [Installation and recovery](../OPERATIONS.md#hosted-coordination-and-recovery)
