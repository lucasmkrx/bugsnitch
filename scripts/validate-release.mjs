// SPDX-License-Identifier: MPL-2.0
import { readFile, readdir } from 'node:fs/promises';
const readJson = async (path) => JSON.parse(await readFile(path, 'utf8'));
export async function validateRelease() {
  const workspace = await readJson('package.json');
  const manifest = await readJson('apps/vscode/package.json');
  const version = workspace.version;
  if (!/^\d+\.\d+\.\d+$/.test(version))
    throw new Error('Release version must be numeric semver');
  for (const folder of ['apps', 'packages']) {
    for (const name of await readdir(folder)) {
      const metadata = await readJson(`${folder}/${name}/package.json`);
      if (metadata.version !== version)
        throw new Error(`Version mismatch: ${folder}/${name}`);
    }
  }
  if (!(await readFile('CHANGELOG.md', 'utf8')).includes(`## ${version}`))
    throw new Error('Missing changelog version');
  const notes = await readFile(`docs/releases/${version}.md`, 'utf8');
  if (!notes.includes(version))
    throw new Error('Release notes do not identify this version');
  if (
    process.argv.includes('--official') &&
    manifest.publisher === 'bugsnitch-dev-placeholder'
  )
    throw new Error('An owner-configured Marketplace publisher is required');
  if (
    manifest.main !== './dist/extension.cjs' ||
    manifest.icon !== 'dist/brand/bugsnitch-icon.png'
  )
    throw new Error('Unexpected packaged entry points');
  console.log(
    `Release metadata valid: ${manifest.publisher}.${manifest.name} ${version}`,
  );
  return manifest;
}
if (process.argv[1]?.endsWith('validate-release.mjs')) await validateRelease();
