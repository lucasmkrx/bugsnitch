// SPDX-License-Identifier: MPL-2.0
import { readFile, writeFile } from 'node:fs/promises';
import { dirname, resolve, join } from 'node:path';
export async function writeBundledNotices(moduleIds, outputDirectory) {
  const packages = new Map();
  for (const id of moduleIds) {
    if (!id.includes('node_modules')) continue;
    let directory = dirname(resolve(id.replaceAll('\0', '').split('?')[0]));
    while (directory.includes('node_modules')) {
      let manifest;
      try {
        manifest = JSON.parse(
          await readFile(join(directory, 'package.json'), 'utf8'),
        );
      } catch {
        directory = dirname(directory);
        continue;
      }
      if (!manifest.name || !manifest.version)
        throw new Error('Bundled package has incomplete metadata');
      const key = `${manifest.name}@${manifest.version}`;
      if (!packages.has(key)) {
        let license;
        for (const filename of [
          'LICENSE',
          'LICENSE.md',
          'LICENSE.txt',
          'LICENSE-MIT',
          'license',
          'license.md',
          'license.txt',
        ]) {
          try {
            license = await readFile(join(directory, filename), 'utf8');
            break;
          } catch {
            /* Try the next conventional notice name. */
          }
        }
        if (!license)
          throw new Error(
            `Bundled ${key} needs an explicit third-party notice`,
          );
        packages.set(key, {
          name: manifest.name,
          version: manifest.version,
          license: manifest.license ?? 'See notice',
          text: license,
        });
      }
      break;
    }
  }
  const sorted = [...packages.values()].sort((a, b) =>
    a.name.localeCompare(b.name),
  );
  await writeFile(
    join(outputDirectory, 'THIRD_PARTY_NOTICES.txt'),
    sorted
      .map((item) => `${item.name} ${item.version}\n${item.text}`)
      .join('\n\n'),
  );
  await writeFile(
    join(outputDirectory, 'BUNDLED_DEPENDENCIES.json'),
    JSON.stringify(
      sorted.map(({ name, version, license }) => ({ name, version, license })),
      null,
      2,
    ) + '\n',
  );
}
