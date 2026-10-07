# Releasing a GitHub preview

GitHub VSIX previews and Marketplace publication are separate. A preview may use the explicitly documented temporary publisher; an official publisher is required before Marketplace publication. A future publisher change changes the extension ID, so users may need to uninstall the preview.

1. Merge reviewed changes with the required CI checks passing. Update all package versions together, the changelog, and `docs/releases/<version>.md`.
2. Complete the [manual checks](validation.md#manual-review) on the packaged VSIX.
3. Run the **Release preview** workflow from `main`. It checks the exact workflow commit, runs the CI matrix, and packages the VSIX on Linux. It refuses an existing release/tag and requires the matching versioned notes file.
4. Download the VSIX and `SHA256SUMS` from the resulting draft release. Install that artifact for a final smoke check; inspect the notes and publish the draft as a prerelease.

The draft workflow's source commit and run URL are appended to the release notes. The release contains the built VSIX and its checksum, not a committed package or dependency tree. Re-run validation for source changes; a packaged artifact must remain associated with its checked source commit.

To check a downloaded checksum with Node on any platform:

```sh
node --input-type=module -e "import {readFileSync} from 'node:fs'; import {createHash} from 'node:crypto'; console.log(createHash('sha256').update(readFileSync('bugsnitch-0.2.0.vsix')).digest('hex'))"
```

Compare the printed value with `SHA256SUMS`. The checksum detects differing bytes; it does not independently establish publisher identity.

Marketplace publication requires a real publisher/account, verified manifest identity, updated package instructions, and a separate publication step. This workflow has no Marketplace token and never publishes there.
