# Validation

## Automated boundaries

| Command                                            | What it establishes                                                                                                                                                                                                                                                                                                                 |
| -------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pnpm lint`, `pnpm format:check`, `pnpm typecheck` | Source conventions, enforced package import boundaries and strict TypeScript.                                                                                                                                                                                                                                                       |
| `pnpm test:coverage`                               | Disposable Git integration, parsers, generated ancestry, editor request ordering, selection, panel replay, allowlists and explicit export. V8 reports Node/core coverage; it does not measure browser or native editor code. Gates: 85% statements/lines, 75% branches, 80% functions.                                              |
| `pnpm test:browser`                                | Actual production panel and bundled React UI with a narrow VS Code API adapter and real Git fixtures. Clicks pagination, parent inspection, range/notes, patches, file history and search; checks keyboard activation/focus, ready replay and axe WCAG A/AA at 420/1280 px in representative dark/light/high-contrast theme tokens. |
| `pnpm test:extension`                              | Development extension in a real isolated VS Code host. Both commands, actual webview readiness and protocol navigation, native diff, file history, notes/export, unchanged HEAD/index/source files.                                                                                                                                 |
| `pnpm test:package`                                | Clean build, VSIX creation, explicit archive/resource/identity/budget validation, SHA-256 receipt, CLI installation of those exact bytes into an isolated profile, then the real host smoke. Checksum verified again afterward.                                                                                                     |
| `pnpm validate:release`                            | Matching workspace versions, versioned notes/changelog and expected packaged entry points. `--official` refuses the placeholder publisher.                                                                                                                                                                                          |
| `pnpm benchmark`                                   | Reproducible warm-cache read/layout measurements in a fresh fixture; methodology and limits in [performance](performance.md).                                                                                                                                                                                                       |

The installed smoke adds an observer runner beside the extracted installation so VS Code scopes its API correctly. Packaged runtime resources/manifest are untouched; a separate empty observer extension launches the runner. The host test sends subsequent navigation through the registered receiver rather than clicking editor webview controls. The Chromium test does click controls, but its adapter does not implement native VS Code dialogs/editors. These complementary tests deliberately cover different boundaries.

Node tests include inherited Git redirection isolation, root patches under ambient configuration, linked/nested/empty/bare roots, merge/shallow/partial history, deleted refs/directories, content and submodule helpers, deadlines, borrower-aware cancellation and kill escalation. Generated graph tests follow drawn lines independently of stored edges. Repository-controlled text is escaped and protocol fields are constrained; no finite suite proves every Git/OS configuration safe.

## Running the checks

Install pinned dependencies, then `pnpm exec playwright install chromium` once. VS Code host tests use [Microsoft’s test runner](https://code.visualstudio.com/api/working-with-extensions/testing-extension); the first run downloads the chosen editor. Test artifacts are ignored. Normal profiles and installed extensions are not reused. `BUGSNITCH_TEST_VSCODE_VERSION` chooses the download version; `BUGSNITCH_TEST_VSCODE_EXECUTABLE` can point at an existing executable. On headless Linux, prefix host/package checks with `xvfb-run -a`.

CI is configured for VS Code 1.105.0 on Linux/macOS/Windows and current stable on Linux. It runs installed package checks on every matrix entry, browser checks on minimum Linux, and publishes coverage/browser/package receipts with the checked VSIX. Actions are pinned to immutable commits. The required aggregate `check` depends on the whole matrix. Configuration does not establish that the changed workflow has already passed remotely.

## Local evidence for 0.3.0

The 7 October 2026 implementation was exercised on macOS x64 with Node 24.20.0, Git 2.39.5, VS Code 1.105.0 and Chromium from Playwright 1.63.0. The exact installed preview passed both commands, native diffs, file history and explicit export while preserving HEAD/index/source bytes. The production browser journey passed all six theme/width axe scans and focus assertions. Its [recording](screenshots/investigation.webm) and [screenshot](screenshots/investigation-current.png) show the actual browser-rendered product with disposable data, not a simulated editor shell.

Latest detailed counts and coverage are recorded in [implementation status](implementation-status.md). The wider CI matrix must run after the source is pushed; local macOS success does not certify Windows/Linux or current-stable execution of these changes.

## Human checks still needed

Install the final checked VSIX and follow the [demo](demo.md) on a small and larger real repository. Review keyboard/screen-reader usability and actual VS Code themes, range explanations, session disposal, missing/filtered/binary cases, notices and publisher identity. The older 0.2.0 source had qualitative checks on four real repositories; those are not a substitute for reviewing the new 0.3.0 workflow.

Run the [outside-user protocol](usability.md) before claiming independent adoption or recruiter/user approval. An actual VS Code window recording also remains a separate capture task; the committed browser recording is labeled honestly.
