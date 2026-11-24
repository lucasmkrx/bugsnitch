# 0004 · Migrate the runner and hosted service to Rust in stages

Accepted on 2026-11-22. The owner selected Rust for both executables. Existing users need to retain journals, device identities, migration history and the dashboard contract through each release.

Start with two independently testable storage boundaries. A persistent Rust child embeds SQLite and handles journal upgrades. A short-lived Rust child links libpq and applies the existing hosted migrations under the same transaction and lock as the original Go version. The Go commands continue to own execution and HTTP routing. Ship matched executables together and check them through their normal entry points.

This introduces a private journal protocol and an extra hosted build dependency on PostgreSQL development tools. It allows each cutover to ship with compatibility and recovery tests. Future stages should remove private process boundaries when their callers move into the same Rust executable. Retain data and protocol compatibility for rollback at each cutover. Measure each workload independently. [Remaining stages](../IMPLEMENTATION_PLAN.md#rust-migration)

The Go HTTP routing described here was the initial storage cutover. The packaged Rust server subsequently took over the public listener and the GitHub App client moved to Rust. [HTTP cutover](0007-rust-http-and-providers.md)
