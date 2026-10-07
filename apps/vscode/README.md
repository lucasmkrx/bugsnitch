# Bugsnitch

**Open-source Git forensics for VS Code.**

> Find what changed. Find where the bug started.

Early development, version 0.3.0. Browse visual local Git ancestry, trace a saved line to its associated commit, and read commit messages, parents, changed files, and patch previews. Line attribution is an investigation lead, not proof of a regression.

- **Bugsnitch: Open** — repository, branch, working-tree summary, recent history, refs, and commit inspection.
- **Bugsnitch: Snitch on Current Line** — author, date, original/current line and file, and an action to inspect the originating commit. Available in the Command Palette and editor context menu.

Requires VS Code 1.105.0+, Git 2.39+ on PATH, and a trusted local Git workspace. Save dirty/untitled editors first. Untracked files, empty repositories, binary content, and local lines without a committed origin receive clear explanations. Text files up to 4 MiB are supported; patches are previewed up to 256 KiB. Merge changes start with the first parent; another parent can be selected. Per-file patches, read-only native diffs and rename-following file history are available. Refresh updates the snapshot; Load more reads another bounded page.

Mark known-good and known-bad commits, validate their ancestor relationship, and freeze up to 200 candidates. Assessments are manual. Save evidence notes in the panel or explicitly export JSON; closing the panel discards the in-memory session. Loaded-commit and ref searches keep navigation local.

All inspection runs locally. No telemetry, accounts, AI API calls, or network requests. Graph lanes remain stable across pagination. Show in graph connects commit inspection to ancestry; parent buttons continue the investigation. Branch and tag entry points browse local objects without checkout or fetch. Selection and line evidence stay available while browsing. The extension does not change Git history, the working tree, or the index.

Configured Git content filters are bypassed, so modified counts may differ from ordinary Git status. Line tracing filtered files is unavailable; commit inspection remains supported. Git signature verification and external diff/textconv/fsmonitor helpers are also disabled.

This development build uses `bugsnitch-dev-placeholder`, an unconfigured temporary publisher value. It is not a Marketplace release.

Source and development instructions: [Bugsnitch repository](https://github.com/lucasmkrx/bugsnitch). Bugs and feature proposals: [Issues](https://github.com/lucasmkrx/bugsnitch/issues). Security reports: follow [SECURITY.md](https://github.com/lucasmkrx/bugsnitch/blob/HEAD/SECURITY.md) and avoid public disclosure of exploitable vulnerabilities.

Software source: MPL-2.0, Copyright © 2026 Lucas Marques. The Bugsnitch name and logo are excluded from the software license; see the bundled TRADEMARKS.md.
