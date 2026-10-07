# Implementation status

This records the project-quality plan implemented on 7 October 2026. Local deliverables and actual results are distinguished from owner actions, remote CI results and human validation. It is not a claim of universal compatibility, independent adoption or completed Marketplace publication.

| Plan item | Delivered work                                                                                                                                                       | State                                                                                  |
| --------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| 1         | Isolated Git fixtures, inherited-environment sentinel regression, failure cleanup                                                                                    | Complete locally; full suite passes                                                    |
| 2         | Deferred ready handshake, full loaded-prefix/detail/evidence/error replay, numeric scroll restoration                                                                | Complete; host and browser replay checks pass                                          |
| 3         | Captured URI/version/text/line, per-document cancellation, monotonic repository ownership                                                                            | Complete; editor boundary and ordering tests pass                                      |
| 4         | Forced root/selected-parent patches, strict NUL/status/blame parsing, usable errors, UTF-8 truncation                                                                | Complete; core and contract regressions pass                                           |
| 5         | Parent status avoids submodule recursion; gitlink patches; nested helper fixtures; shared cancellation/deadlines/escalation                                          | Complete; subprocess and real-repository checks pass                                   |
| 6         | Production browser interaction harness and exact checked VSIX install in isolated editor profile                                                                     | Implemented; local journey passes; remote results linked below                         |
| 7         | Theme-aware search/actions/forms, collapsible range panel, focus, parent text alternative, six axe theme/width scans                                                 | Complete for automated scope; outside screen-reader review remains a human check       |
| 8         | Per-file patches, arbitrary actual merge parents, native read-only blob diffs, rename-following bounded history                                                      | Complete; core/browser/installed-host checks pass                                      |
| 9         | Ancestor-validated frozen endpoints/candidates, manual verdicts/notes, explicit save-dialog JSON export                                                              | Complete; candidate/path allowlists, replay and export checks pass                     |
| 10        | Deterministic generated DAG/prefix checks; disposable 1,500-commit benchmark and documented timings/limits                                                           | Complete; measurements in performance report                                           |
| 11        | Command/editor/repository selection checks; linked/nested/symlink/empty/bare/shallow/partial fixtures; explicit contract                                             | Implemented; compatibility matrix runs remotely; results linked below                  |
| 12        | Clean staging, derived notices/inventory, explicit VSIX allowlist, metadata/checksum validation, coverage gates, action SHA pins, import boundaries and CI artifacts | Implemented; remote CI and CodeQL results linked below                                 |
| 13        | Official distribution preparation and publisher/identity migration instructions; official-mode placeholder refusal                                                   | Preparation complete; permanent publisher/account and publication require owner action |
| 14        | Updated README, engineering case study, demo, current production-webview screenshot and actual browser recording                                                     | Repository portfolio complete; actual VS Code window recording remains a capture task  |
| 15        | Contributor checks, scoped issue backlog, participant protocol and honest results template                                                                           | Preparation complete; independent participants and actual observations still required  |

## Latest local validation

- 56 tests across 18 files passed. V8 Node/core coverage: 88.36% statements, 87.56% branches, 87.97% functions, 89.88% lines. Native editor/browser tests are separate and not included in those percentages.
- Production Chromium journey passed, including six axe WCAG A/AA scans across 420/1280 px and representative dark/light/high-contrast tokens, keyboard activation/focus, escaped hostile metadata and ready replay.
- The exact installed 0.3.0 VSIX passed VS Code 1.105.0 on macOS x64: both commands, real webview readiness, pagination, parent/ref/focus navigation, file history, native historical diff and explicit export. HEAD, index and working source bytes remained unchanged.
- Validated archive: 16 allowed entries, approximately 126.53 KiB. SHA-256: `36adfdf748fe087b53cf0eadde6a7c2ebf0bf4ca1d23e67cace3c8d37fadf9be`. This identifies the current local artifact; rebuilding a ZIP can change its checksum. The validator writes the authoritative current receipt to ignored `.cache/package-validation.json`.
- Strict TypeScript and lint pass; formatting, release metadata and diff checks are part of the final local review. Build output and VSIX remain ignored artifacts.
- Benchmark results, fixture shape, cache mode and memory caveats are documented in [performance](performance.md). Local environment: Node 24.20.0, Git 2.39.5 (Apple Git-154), macOS x64. No production throughput claim is made.

## Remote validation

[PR #6](https://github.com/lucasmkrx/bugsnitch/pull/6) records CI and CodeQL checks for the source and the merge decision. The required `check` gate combines minimum-version Linux/macOS/Windows and current-stable Linux jobs; each runs Node checks, native editor tests and the installed-package smoke. The minimum Linux job also runs the production browser journey and uploads reports and the validated VSIX. Use the checks on the final PR commit for the authoritative result; earlier attempts remain visible in its history.

The first remote run caught a Windows `.cmd` launch failure in the package test and a latest-stable headless Linux renderer startup failure. The harness now invokes the Windows CLI through Electron's Node mode without a shell and disables hardware GPU use and avoids `/dev/shm` for Linux CI. These changes retain the full test matrix and required merge gate. Dependabot keeps the declared Node and minimum VS Code types; compiler major upgrades require a parser/tooling compatibility review. No release was published by this implementation session.

## Remaining external completion conditions

1. Owner supplies and controls a permanent Marketplace publisher, completes identity migration, validates the rebuilt official package and publishes through that account. The source retains `bugsnitch-dev-placeholder`; no listing or publisher was invented. See [releasing](releasing.md).
2. Capture a real VS Code window demonstration and run the outside-user protocol with actual participants, including assistive-technology feedback where possible. The existing browser recording is labeled as a test-adapter journey, and the feedback table remains explicitly unmeasured. See [demo](demo.md) and [usability](usability.md).

Investigation sessions remain in panel memory. Only scroll position uses the webview state API. Explicit export is the sole investigation UI filesystem write; native diffs read blobs without checkout. No automatic regression classifier, symbol history, bisect, backend service or hosted integration was added or advertised.
