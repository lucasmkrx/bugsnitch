// SPDX-License-Identifier: MPL-2.0
import { createRequire } from 'node:module';
import { readFile, writeFile, mkdir, stat } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { validateRelease } from './validate-release.mjs';
const require = createRequire(import.meta.url);
// Use the ZIP reader declared by the pinned packaging tool, solely for development validation.
const { open } = createRequire(require.resolve('@vscode/vsce'))('yauzl');
const manifest = await validateRelease();
const path =
  process.argv[2] ?? `apps/vscode/bugsnitch-${manifest.version}.vsix`;
if ((await stat(path)).size > 2 * 1024 * 1024)
  throw new Error(
    'VSIX exceeds the 2 MiB package budget; review the inventory',
  );
const zip = await new Promise((resolve, reject) =>
  open(path, { lazyEntries: true }, (error, archive) =>
    error ? reject(error) : resolve(archive),
  ),
);
const files = new Map();
await new Promise((resolve, reject) => {
  zip.on('error', reject);
  zip.on('end', resolve);
  zip.on('entry', (entry) => {
    if (entry.uncompressedSize > 4 * 1024 * 1024 || files.size > 100) {
      zip.close();
      reject(new Error('Unexpected package size or file count'));
      return;
    }
    zip.openReadStream(entry, (error, stream) => {
      if (error) {
        reject(error);
        return;
      }
      const chunks = [];
      stream.on('data', (chunk) => chunks.push(chunk));
      stream.on('error', reject);
      stream.on('end', () => {
        if (files.has(entry.fileName)) {
          reject(new Error('Duplicate package entry'));
          return;
        }
        files.set(entry.fileName, Buffer.concat(chunks));
        zip.readEntry();
      });
    });
  });
  zip.readEntry();
});
const allowed =
  /^(?:\[Content_Types\]\.xml|extension\.vsixmanifest|extension\/(?:package\.json|README\.md|CHANGELOG\.md|LICENSE|TRADEMARKS\.md|dist\/(?:extension\.cjs|THIRD_PARTY_NOTICES\.txt|BUNDLED_DEPENDENCIES\.json|webview\/webview\.(?:js|css)|brand\/(?:README\.md|bugsnitch-logo(?:-dark)?\.svg|bugsnitch-icon\.png))))$/;
for (const name of files.keys())
  if (
    !allowed.test(
      name
        .replace('extension/readme.md', 'extension/README.md')
        .replace('extension/changelog.md', 'extension/CHANGELOG.md')
        .replace('extension/LICENSE.txt', 'extension/LICENSE'),
    )
  )
    throw new Error(`Unexpected package entry: ${name}`);
for (const name of [
  'package.json',
  'LICENSE',
  'TRADEMARKS.md',
  'README.md',
  'dist/extension.cjs',
  'dist/webview/webview.js',
  'dist/webview/webview.css',
  'dist/brand/bugsnitch-icon.png',
  'dist/THIRD_PARTY_NOTICES.txt',
  'dist/BUNDLED_DEPENDENCIES.json',
])
  if (
    !(
      files.get(`extension/${name}`) ??
      files.get(`extension/${name.toLowerCase()}`) ??
      (name === 'LICENSE' ? files.get('extension/LICENSE.txt') : undefined)
    )?.length
  )
    throw new Error(`Required package resource missing: ${name}`);
const packaged = JSON.parse(
  files.get('extension/package.json').toString('utf8'),
);
for (const key of ['name', 'version', 'publisher', 'main', 'icon'])
  if (packaged[key] !== manifest[key])
    throw new Error(`Packaged ${key} differs from checked source`);
const xml = files.get('extension.vsixmanifest')?.toString('utf8') ?? '';
if (
  !xml.includes(`Version="${manifest.version}"`) ||
  !xml.includes(`Publisher="${manifest.publisher}"`)
)
  throw new Error('VSIX XML identity differs from the checked manifest');
const sha256 = createHash('sha256')
  .update(await readFile(path))
  .digest('hex');
await mkdir('.cache', { recursive: true });
await writeFile(
  '.cache/package-validation.json',
  JSON.stringify(
    { version: manifest.version, sha256, files: [...files.keys()].sort() },
    null,
    2,
  ) + '\n',
);
console.log(`Validated ${files.size} package entries; SHA-256 ${sha256}`);
