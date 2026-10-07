# Investigation behavior and limits

Selection, commit detail, and line evidence survive refresh, pagination, and history entry-point changes. If a selected commit is outside the loaded graph, **Show in graph** opens a bounded history anchored at that commit. **Return to previous history** takes you back to the prior entry point. **Current checkout (HEAD)** returns to the actual checkout's history.

History pages use a frozen commit ID, so a moving branch does not mix snapshots between pages. Refresh updates branch/tag tips and the working-tree summary. The selector lists up to 2,000 local refs and reports truncation. Tags pointing to blobs or trees are omitted; annotated commit tags are peeled. No checkout or fetch occurs. The repository overview always describes the actual checkout.

Parents outside loaded history have dashed continuations. Available ancestry is limited by the local clone, including shallow-history boundaries. Missing local objects produce an error instead of triggering a fetch.

Save dirty or untitled editors before tracing a line. Saved working-tree changes are supported: if the selected line has no committed origin, Bugsnitch says so. Binary files and text files larger than 4 MiB are excluded. Patch previews retain at most 256 KiB and disclose truncation; merge patches compare against the first parent.

Git content filters are bypassed. Filtered files are excluded from line tracing, and repositories with configured filters show a warning that modified counts may differ from ordinary Git status. Commit inspection remains available. External diff, textconv, fsmonitor, signature verification, and implicit remote fetch helpers are disabled. See [architecture](architecture.md) for the execution boundary.

A line-associated commit is an investigation lead. Later changes, configuration, dependencies, or interactions between commits can also explain a regression. Bugsnitch does not currently identify regression-introducing commits automatically.
