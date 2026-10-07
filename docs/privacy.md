# Privacy

Bugsnitch 0.1.0 is local first. The extension runs system Git in the repository selected from your editor or workspace. It reads repository metadata, the index/status, commit history, changed filenames, textual diffs, and line blame. The active saved document is passed to Git over stdin to match current line numbers; it is not uploaded or saved as a temporary file by Bugsnitch.

There is no analytics, telemetry, tracking, authentication, Bugsnitch account, AI API, cloud upload, hosted indexing, hidden network request, or dependency on bugsnitch.com. The webview blocks network connections with Content Security Policy. Runtime inspection works offline. Installing dependencies, GitHub CI, and future manual Marketplace distribution are separate developer/distribution activities, not extension runtime data collection.

Investigation data stays in extension/webview memory. No investigation database or webview persistent state is created. Closing the panel releases its state and cancels outstanding processes. Refresh reads a new repository snapshot. Git may read its own local repository configuration and objects; Bugsnitch does not fetch, contact remotes, execute configured diff/textconv/fsmonitor/content-filter/signature helpers, or mutate history/index.

Content filters are disabled for inspection. In repositories that configure them, Bugsnitch warns that modified counts may differ from ordinary Git status. Line tracing of files with filter attributes is unavailable rather than executing a helper or guessing provenance from transformed content. Commit inspection remains available.

Lazy fetching in partial clones is disabled and all Git transports are denied for inspection processes. Missing local objects produce an error; Bugsnitch does not download them or run remote transport helpers.

The extension requires workspace trust. Commit messages, author names, refs, paths, and patches are repository-controlled text. React renders them as text; the webview cannot request arbitrary Git commands or filesystem access. Do not share screenshots or logs containing confidential code when filing issues.

Any future network integration must be optional, explicit, documented, and isolated from the local Git/forensics packages. A future external cloud service is not part of this repository or release.
