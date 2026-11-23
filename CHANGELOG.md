# Changelog

## Unreleased rebuild

The Rust and Go service, local runner and React dashboard replace the extension. The entries under numbered releases describe the earlier extension. [Verification ledger](docs/PROGRESS.md)

- Each environment check now belongs to a specific deployment. Deploying the same revision again requires a fresh check. Migration 022 preserves historical evidence and binds validations with an unambiguous deployment history.
- Workers recheck prerequisite revisions before taking or renewing authority. A timed-out model turn can release its reservation even when the grant acknowledgement was lost. Foreground sync and verification checks respond to process signals.
- Local ticket admission handles concurrent capacity changes. The runner bounds Git output and reads tracked filenames literally, including non-ASCII characters.
- Plan publication retries definite rate-limit rejections. The dashboard renders older plans, waits for slow reads and offers a read-only retry after a successful save.
- `make checkout-acceptance` reproduces and repairs the bundled checkout, verifies two deployments in Chromium and exercises a recurrence using local fixtures.

## 0.3.0 — Preview

- Compare any merge parent, preview one file, open read-only historical VS Code diffs, and follow file history across renames.
- Freeze an ancestor-validated known-good/known-bad range; add manual verdicts and evidence notes with explicit JSON export.
- Restore complete panel state after webview recreation and capture immutable, ordered editor requests.
- Harden root-commit patches, malformed Git output, shared cancellation, deadlines, inherited Git environment and submodule helper suppression.
- Add production-browser accessibility checks, an exact installed-VSIX smoke, generated ancestry checks, measured benchmarks, clean build inventory and dependency-derived notices.
- Keep previews under the temporary publisher until the owner establishes a permanent Marketplace identity.

## 0.2.0 — Preview

- Draw real parent ancestry beside commit history, including branches and merges, with stable lanes across pagination and explicit continuations at page boundaries.
- Preserve line evidence and commit selection across history reads; add parent inspection, Show in graph, and bounded history from a selected commit.
- Label line attribution as an investigation lead, without claiming it proves a regression.
- Add local branch, remote-tracking branch, and commit-tag history entry points, including peeled annotated tags; preserve a return path from commit focus to the selected entry point.
- Freeze paginated history to its selected tip and update refs only on refresh; handle disappeared refs without mixing snapshots.
- Add dark-background branding, a guided disposable investigation, and an engineering case study.
- Automate both commands and read-only navigation inside VS Code; validate the minimum host on Linux/macOS/Windows and current stable on Linux.
- Prepare GitHub preview VSIX releases with checksums and linked validation runs. Marketplace publication still requires an official publisher.

## 0.1.0 — 2026-10-06

- Reboot the existing repository as an open-source VS Code Git-forensics extension; preserve repository history.
- Add bounded recent history, repository/branch/status summaries, refs, and commit message/file/patch inspection.
- Add local Snitch on Current Line with exact-line blame, original/current locations, and commit enrichment.
- Establish independent Git, graph-model, forensics, UI, and shared-contract packages.
- Add tests, strict TypeScript, lint/format/build tooling, CI, and community documentation.
- License source under MPL-2.0 and document separate Bugsnitch brand terms.

This is early development. Regression ranking, bisect, integrations, and optional intelligence are future work.
