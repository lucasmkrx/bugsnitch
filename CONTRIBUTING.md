# Contributing to Bugsnitch

Constructive changes are welcome. Discuss substantial product/architecture proposals in [Issues](https://github.com/lucasmkrx/bugsnitch/issues) before implementing them, and follow [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## Development

Install system Git and Node 24 LTS (`.nvmrc`), enable Corepack, and use pinned pnpm 11.25.0. From the root:

```sh
corepack enable
pnpm install --frozen-lockfile
pnpm build
```

Open the root in VS Code and press F5 using **Bugsnitch Extension**. Open a trusted fixture workspace in the development host. Use `pnpm dev` to watch both bundles; reload the host to pick up changes.

Before submitting:

```sh
pnpm format
pnpm lint
pnpm format:check
pnpm typecheck
pnpm test
pnpm build
```

`pnpm package` additionally validates local VSIX packaging. It does not publish anything. The publisher placeholder must be replaced for an official release.

## Boundaries and tests

Keep VS Code APIs in `apps/vscode`, Git invocation/parsing in `packages/git`, investigation logic in `packages/forensics`, graph concepts in `packages/graph`, presentation in `packages/ui`, and protocol/types in `packages/shared`. Favor small modules and pure parsers. Use explicit process argument arrays, bounded asynchronous reads, validated messages, and escaped text. Keep the local core offline and read-only. Do not add telemetry, cloud prerequisites, or destructive Git operations as incidental changes.

Add tests for meaningful behavior and edge cases. Integration tests should create and remove temporary repositories, set their own identity, and avoid user repositories/global configuration. For UI changes, check keyboard use and light/dark/high-contrast themes in VS Code. Describe validation and limits in your PR.

## Pull requests

Create a focused branch, explain the problem and final behavior, link related issues, and complete the PR template. Include only necessary source/docs/assets; keep secrets, local settings, builds, and VSIX packages out of commits. Maintainers may request scope or test changes before merging.

Contributions to MPL-covered source are submitted under **MPL-2.0**, the same license as the existing source (inbound equals outbound). Use concise `SPDX-License-Identifier: MPL-2.0` notices in original source files. There is no CLA or copyright ownership transfer. Brand assets have separate terms in [TRADEMARKS.md](TRADEMARKS.md); software contribution rights do not grant rights to use the Bugsnitch identity for a fork.
