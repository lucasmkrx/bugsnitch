# Bugsnitch

**Open-source Git forensics for VS Code.**

> Find what changed. Find where the bug started.

Early development, version 0.1.0. Inspect local history, trace a saved line to its originating commit, and read commit messages, changed files, and patch previews.

- **Bugsnitch: Open** — repository, branch, working-tree summary, recent history, refs, and commit inspection.
- **Bugsnitch: Snitch on Current Line** — author, date, original/current line and file, and an action to inspect the originating commit. Available in the Command Palette and editor context menu.

Requires VS Code 1.105.0+, system Git on PATH, and a trusted local Git workspace. Save dirty/untitled editors first. Untracked files, empty repositories, binary content, and local lines without a committed origin receive clear explanations. Text files up to 4 MiB are supported; patches are previewed up to 256 KiB. Merge changes compare with the first parent. Refresh updates the snapshot; Load more reads another bounded page.

All inspection runs locally. No telemetry, accounts, AI API calls, or network requests. The extension does not change Git history or the index. The initial interface is a history list; graph lane visualization is future work.

Configured Git content filters are bypassed, so modified counts may differ from ordinary Git status. Line tracing filtered files is unavailable; commit inspection remains supported. Git signature verification and external diff/textconv/fsmonitor helpers are also disabled.

This development build uses `bugsnitch-dev-placeholder`, an unconfigured temporary publisher value. It is not a Marketplace release.

Source and development instructions: [Bugsnitch repository](https://github.com/lucasmkrx/bugsnitch). Bugs and feature proposals: [Issues](https://github.com/lucasmkrx/bugsnitch/issues). Security reports: follow [SECURITY.md](https://github.com/lucasmkrx/bugsnitch/blob/HEAD/SECURITY.md) and avoid public disclosure of exploitable vulnerabilities.

Software source: MPL-2.0, Copyright © 2026 Lucas Marques. The Bugsnitch name and logo are excluded from the software license; see the bundled TRADEMARKS.md.
