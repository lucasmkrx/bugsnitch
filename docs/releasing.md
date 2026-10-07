# Releasing a GitHub preview

GitHub VSIX previews and Marketplace publication are separate. A preview may use the explicitly documented temporary publisher; an official publisher is required before Marketplace publication. A future publisher change changes the extension ID, so users may need to uninstall the preview.

1. Merge reviewed changes with the required CI checks passing. Update all package versions together, the changelog, and `docs/releases/<version>.md`.
2. Complete the [manual checks](validation.md#manual-review) on the packaged VSIX.
3. Run the **Release preview** workflow from `main`. It checks the exact workflow commit, runs the CI matrix, and packages the VSIX on Linux. It refuses an existing release/tag and requires the matching versioned notes file.
4. Download the VSIX and `SHA256SUMS` from the resulting draft release. Install that artifact for a final smoke check; inspect the notes and publish the draft as a prerelease.

The draft workflow's source commit and run URL are appended to the release notes. The release contains the built VSIX and its checksum, not a committed package or dependency tree. Re-run validation for source changes; a packaged artifact must remain associated with its checked source commit.

To check a downloaded checksum with Node on any platform:

```sh
node --input-type=module -e "import {readFileSync} from 'node:fs'; import {createHash} from 'node:crypto'; console.log(createHash('sha256').update(readFileSync('bugsnitch-0.3.0.vsix')).digest('hex'))"
```

Compare the printed value with `SHA256SUMS`. The checksum detects differing bytes; it does not independently establish publisher identity.

Marketplace publication requires a real publisher/account, verified manifest identity, updated package instructions, and a separate publication step. This workflow has no Marketplace token and never publishes there.

## Checked package and official identity

Run `pnpm validate:release` and `pnpm test:package` before distribution. The latter validates the archive's explicit resource inventory and identity, then installs the exact VSIX in a disposable profile and verifies its checksum before and after the smoke. The CI matrix repeats this on the minimum supported hosts and current-stable Linux. Rebuild/retest after any manifest, source, documentation or dependency change that affects packaged files.

For an official Marketplace release, the owner must create/control a permanent publisher and provide its exact ID. Publisher IDs are permanent and appear in the extension identity. Follow the current [official publishing guide](https://code.visualstudio.com/api/working-with-extensions/publishing-extension) and [publisher management](https://marketplace.visualstudio.com/manage). Manual upload of a checked VSIX is supported; use owner-managed authentication rather than putting credentials into source or chat.

1. Replace the manifest placeholder with the verified permanent publisher, then update installation/migration copy and rerun `node scripts/validate-release.mjs --official`.
2. Prepare a new identity-specific checked VSIX and validate/install it again. Users of the placeholder preview must uninstall that extension before switching; the identities do not update each other.
3. Review the packaged README, icon, repository/license/bugs metadata and final notes. The package includes no remote runtime dependency or unadvertised tracking.
4. Upload the checked artifact through the controlled publisher account and verify the listing and installation URL. Record the resulting official ID/URL and remove preview-only copy only after that succeeds.

This preparation does not create an account, claim a publisher, store a token or publish an extension. No permanent ID was supplied during implementation, so the source intentionally remains an honest GitHub preview. The 0.3.0 local VSIX is a candidate artifact; it is not claimed as a remotely published release.
