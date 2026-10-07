# Validation

## Automated checks

`pnpm lint`, `pnpm format:check`, `pnpm typecheck`, `pnpm test`, and `pnpm package` validate source, contracts, Git behavior, graph layout, and package creation. Tests use disposable Git repositories, including merge, shallow clone, partial clone, moved/deleted ref, and configured-helper fixtures.

`pnpm test:extension` builds the production bundles, creates a disposable demo repository, and launches an isolated VS Code through [Microsoft's test runner](https://code.visualstudio.com/api/working-with-extensions/testing-extension). Its first run downloads VS Code 1.105.0, the minimum supported version. CI also checks current stable VS Code on Linux. Test-runner downloads are development tooling; the Bugsnitch runtime performs no network requests.

The host check executes **Open** and **Snitch on Current Line** through VS Code's command API. It waits for the real webview's ready message, observes outgoing host messages, and sends subsequent navigation messages through the registered receiver. It checks pagination, expected line attribution and patch, parent inspection, branch/annotated-tag navigation, commit focus, return history, rejection of an unknown ref, and unchanged HEAD, index bytes, and working file contents.

This is an integration check of the editor, production bundle, Git, and host/webview contract. It does not click rendered controls or inspect pixels. Component/state tests and manual UI review cover different boundaries.

The test profile and repository are temporary and removed on exit. Downloaded VS Code copies are cached in ignored `.vscode-test/`; the compiled test runner is in ignored `.cache/`. Normal settings and installed extensions are not reused. To use an already installed VS Code executable locally, set `BUGSNITCH_TEST_VSCODE_EXECUTABLE` to its absolute executable path. To choose another downloaded version, set `BUGSNITCH_TEST_VSCODE_VERSION`.

On headless Linux, run `xvfb-run -a pnpm test:extension`. CI runs the minimum supported VS Code on Linux, macOS, and Windows, plus current stable on Linux. The required `check` job succeeds only when every matrix job passes. A matrix describes the checks we run, rather than proof of support for every OS version, CPU, or repository shape.

## Manual review

Before a release, install its VSIX and test a small repo, a larger repo, and a disposable tagged/merged fixture. Follow the [demo](demo.md) and verify:

- Light, dark, and high-contrast readability, keyboard focus, and commit inspection.
- Context remains visible through refresh, pagination, branch/tag changes, and parent navigation.
- Dirty, untracked, empty, filtered, and missing-object cases explain their limits.
- Package identity, licenses, brand terms, and packaged resources match the release.

The 0.2.0 development build was manually exercised on four real repositories and a disposable annotated-tag fixture, including both commands, graph focus, and unchanged checkout/index/files. Light, dark, and high-contrast themes were checked. These are qualitative observations; no production benchmark or measured throughput claim is published.
