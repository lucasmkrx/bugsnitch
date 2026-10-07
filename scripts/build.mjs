// SPDX-License-Identifier: MPL-2.0
import { cp, mkdir, readFile, writeFile, rm } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { context, build } from 'esbuild';
import { build as buildWebview } from 'vite';
import { Resvg } from '@resvg/resvg-js';
import { writeBundledNotices } from './bundled-notices.mjs';

const root = fileURLToPath(new URL('../', import.meta.url));
process.chdir(root);
const watch = process.argv.includes('--watch');
const app = `${root}apps/vscode`;
if (!watch) await rm(`${app}/dist`, { recursive: true, force: true });
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
  metafile: true,
  logLevel: 'info',
  legalComments: 'eof',
  banner: { js: '// SPDX-License-Identifier: MPL-2.0' },
};
let hostModules = [];
if (watch) {
  const host = await context(options);
  await host.watch();
} else hostModules = Object.keys((await build(options)).metafile.inputs);
await buildWebview({
  root: app,
  plugins: [
    {
      name: 'bundled-notices',
      async generateBundle(_options, bundle) {
        const modules = Object.values(bundle).flatMap((chunk) =>
          chunk.type === 'chunk' ? Object.keys(chunk.modules) : [],
        );
        await writeBundledNotices([...hostModules, ...modules], `${app}/dist`);
      },
    },
  ],
  configFile: `${app}/vite.config.mts`,
  build: { watch: watch ? {} : null },
});
