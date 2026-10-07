// SPDX-License-Identifier: MPL-2.0
import { cp, mkdir, readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { context, build } from 'esbuild';
import { build as buildWebview } from 'vite';
import { Resvg } from '@resvg/resvg-js';

const root = fileURLToPath(new URL('../', import.meta.url));
process.chdir(root);
const watch = process.argv.includes('--watch');
const app = `${root}apps/vscode`;
await mkdir(`${app}/dist/brand`, { recursive: true });
await cp('assets/brand', `${app}/dist/brand`, { recursive: true });
const svg = await readFile('assets/brand/bugsnitch-logo.svg');
const png = new Resvg(svg, {
  fitTo: { mode: 'width', value: 512 },
  background: 'white',
})
  .render()
  .asPng();
await writeFile(`${app}/dist/brand/bugsnitch-icon.png`, png);
const appRequire = createRequire(`${app}/package.json`);
const reactDomPackage = appRequire.resolve('react-dom/package.json');
const schedulerRequire = createRequire(reactDomPackage);
const bundledPackages = [
  ['React', appRequire.resolve('react/package.json')],
  ['React DOM', reactDomPackage],
  ['Scheduler', schedulerRequire.resolve('scheduler/package.json')],
];
const notices = await Promise.all(
  bundledPackages.map(
    async ([name, path]) =>
      `${name}\n${await readFile(join(dirname(path), 'LICENSE'), 'utf8')}`,
  ),
);
await writeFile(`${app}/dist/THIRD_PARTY_NOTICES.txt`, notices.join('\n\n'));
for (const file of ['LICENSE', 'TRADEMARKS.md', 'CHANGELOG.md'])
  await cp(file, `${app}/${file}`);
const options = {
  entryPoints: [`${app}/src/extension.ts`],
  outfile: `${app}/dist/extension.cjs`,
  bundle: true,
  platform: 'node',
  format: 'cjs',
  target: 'node22',
  external: ['vscode'],
  sourcemap: true,
  logLevel: 'info',
  legalComments: 'eof',
  banner: { js: '// SPDX-License-Identifier: MPL-2.0' },
};
if (watch) {
  const host = await context(options);
  await host.watch();
} else await build(options);
await buildWebview({
  root: app,
  configFile: `${app}/vite.config.mts`,
  build: { watch: watch ? {} : null },
});
