# Investigation behavior and limits

Selection, commit detail, and line evidence survive refresh, pagination, and history entry-point changes. If a selected commit is outside the loaded graph, **Show in graph** opens a bounded history anchored at that commit. **Return to previous history** takes you back to the prior entry point. **Current checkout (HEAD)** returns to the actual checkout's history.

History pages use a frozen commit ID, so a moving branch does not mix snapshots between pages. Refresh updates branch/tag tips and the working-tree summary. The selector lists up to 2,000 local refs and reports truncation. Tags pointing to blobs or trees are omitted; annotated commit tags are peeled. No checkout or fetch occurs. The repository overview always describes the actual checkout.

Parents outside loaded history have dashed continuations. Available ancestry is limited by the local clone, including shallow-history boundaries. Missing local objects produce an error instead of triggering a fetch.

Save dirty or untitled editors before tracing a line. Saved working-tree changes are supported: if the selected line has no committed origin, Bugsnitch says so. Binary files and text files larger than 4 MiB are excluded. Patch previews retain at most 256 KiB and disclose truncation; merge patches initially compare against the first parent; the parent selector can choose another actual parent.

Git content filters are bypassed. Filtered files are excluded from line tracing, and repositories with configured filters show a warning that modified counts may differ from ordinary Git status. Commit inspection remains available. External diff, textconv, fsmonitor, signature verification, and implicit remote fetch helpers are disabled. See [architecture](architecture.md) for the execution boundary.

A line-associated commit is an investigation lead. Later changes, configuration, dependencies, or interactions between commits can also explain a regression. Bugsnitch does not currently identify regression-introducing commits automatically.

## File evidence and range investigations

From commit details, **Patch** previews only that changed path, **Open diff** opens read-only historical contents in a separate VS Code editor group, **History** follows up to 100 commits from the selected frozen tip using Git’s rename tracking, and **Copy path** copies that repository-relative path. Native diffs support textual blobs up to 4 MiB per side, additions and deletions; binary files, trees and gitlinks are explained instead. Rename patches deliberately show addition/deletion records; rename history follows Git’s heuristic and is not an exhaustive merge-aware symbol history.

Mark commits **known good** and **known bad**, then choose **Investigate range**. Good must be a different ancestor of bad in complete local history. The host freezes full IDs and the newest 200 reachable candidates in `good..bad`, including merged side history. This is a candidate set, not a first-parent timeline or automatic causality verdict. Narrow a truncated range before claiming a complete review. Updating an endpoint clears prior assessments.

Inspect candidates, choose your own verdict and save reproduction notes. Saved notes survive refresh and view recreation in the same panel. Closing the panel discards them unless explicitly exported; there is no import or persistent investigation database. Export omits repository roots and patches, but your notes and commit metadata must still be reviewed before sharing.

**Find a loaded commit** searches only the loaded prefix by message, author, hash and refs, showing up to 30 matches. The graph remains intact. **Find a branch or tag** filters the host-issued reference list while retaining the current selected ref. Both controls are local and perform no fetch.
