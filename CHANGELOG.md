# Changelog

## 0.2.0 — Unreleased

- Draw real parent ancestry beside commit history, including branches and merges, with stable lanes across pagination and explicit continuations at page boundaries.
- Preserve line evidence and commit selection across history reads; add parent inspection, Show in graph, and bounded history from a selected commit.
- Label line attribution as an investigation lead, without claiming it proves a regression.
- Add local branch, remote-tracking branch, and commit-tag history entry points, including peeled annotated tags; preserve a return path from commit focus to the selected entry point.
- Freeze paginated history to its selected tip and update refs only on refresh; handle disappeared refs without mixing snapshots.

## 0.1.0 — 2026-10-06

- Reboot the existing repository as an open-source VS Code Git-forensics extension; preserve repository history.
- Add bounded recent history, repository/branch/status summaries, refs, and commit message/file/patch inspection.
- Add local Snitch on Current Line with exact-line blame, original/current locations, and commit enrichment.
- Establish independent Git, graph-model, forensics, UI, and shared-contract packages.
- Add tests, strict TypeScript, lint/format/build tooling, CI, and community documentation.
- License source under MPL-2.0 and document separate Bugsnitch brand terms.

This is early development. Regression ranking, bisect, integrations, and optional intelligence are future work.
