// SPDX-License-Identifier: MPL-2.0
import { build } from 'esbuild';
import { mkdir } from 'node:fs/promises';
await mkdir('.cache', { recursive: true });
await build({
  entryPoints: ['scripts/benchmark.ts'],
  outfile: '.cache/benchmark.mjs',
  bundle: true,
  platform: 'node',
  format: 'esm',
  target: 'node24',
});
await import('../.cache/benchmark.mjs');
