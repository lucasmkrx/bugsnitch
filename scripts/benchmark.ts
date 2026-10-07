// SPDX-License-Identifier: MPL-2.0
import { spawnSync } from 'node:child_process';
import { cpus, platform, arch } from 'node:os';
import { performance } from 'node:perf_hooks';
import { mkdir, writeFile, rm } from 'node:fs/promises';
import {
  repositorySnapshot,
  recentHistory,
  inspectCommit,
  fileHistory,
  regressionRange,
} from '../packages/git/src/index';
import { investigateLine } from '../packages/forensics/src/index';
import { createCommitGraph } from '../packages/graph/src/index';
import { createRepository, fixtureGit } from '../packages/git/test/fixture';
import type { Commit } from '../packages/shared/src/index';

const root = await createRepository();
const timings: {
  operation: string;
  medianMs: number;
  p95Ms: number;
  samples: number;
}[] = [];
async function measure(
  operation: string,
  action: () => unknown | Promise<unknown>,
  samples = 12,
) {
  await action(); // Warm process and filesystem caches; fixture setup is excluded.
  const values: number[] = [];
  for (let i = 0; i < samples; i++) {
    const start = performance.now();
    await action();
    values.push(performance.now() - start);
  }
  values.sort((a, b) => a - b);
  timings.push({
    operation,
    medianMs: Number(values[Math.floor(values.length / 2)]!.toFixed(2)),
    p95Ms: Number(values[Math.ceil(values.length * 0.95) - 1]!.toFixed(2)),
    samples,
  });
}
try {
  let input = '';
  for (let i = 1; i <= 1500; i++) {
    const contents = `export const value = ${i};\n`;
    input += `commit refs/heads/main\nmark :${i}\nauthor Benchmark <bench@example.invalid> 1767355200 +0000\ncommitter Benchmark <bench@example.invalid> 1767355200 +0000\ndata ${Buffer.byteLength(`Commit ${i}`)}\nCommit ${i}\n${i > 1 ? `from :${i - 1}\n` : ''}M 100644 inline calculation.ts\ndata ${Buffer.byteLength(contents)}\n${contents}\n`;
  }
  const result = spawnSync('git', ['-C', root, 'fast-import', '--quiet'], {
    input,
    env: {
      ...Object.fromEntries(
        Object.entries(process.env).filter(
          ([key]) => !key.toUpperCase().startsWith('GIT_'),
        ),
      ),
      GIT_CONFIG_NOSYSTEM: '1',
      GIT_CONFIG_GLOBAL: process.platform === 'win32' ? 'NUL' : '/dev/null',
    },
    encoding: 'utf8',
  });
  if (result.status !== 0)
    throw new Error('Disposable benchmark fixture creation failed');
  await fixtureGit(root, 'reset', '--hard', 'main');
  const tip = await fixtureGit(root, 'rev-parse', 'HEAD');
  const good = await fixtureGit(root, 'rev-parse', 'HEAD~150');
  await measure('snapshot', () => repositorySnapshot(root));
  await measure('history page 100', () =>
    recentHistory(root, { limit: 100, head: tip }),
  );
  await measure('commit + patch', () => inspectCommit(root, tip));
  await measure('line attribution + commit', () =>
    investigateLine({
      repository: root,
      path: 'calculation.ts',
      line: 1,
      contents: 'export const value = 1500;\n',
    }),
  );
  await measure('file history 100', () =>
    fileHistory(root, tip, 'calculation.ts'),
  );
  await measure('ancestor range 150', () => regressionRange(root, good, tip));
  const linear: Commit[] = Array.from({ length: 5000 }, (_, i) => ({
    hash: String(i),
    shortHash: String(i),
    parents: i < 4999 ? [String(i + 1)] : [],
    author: 'Benchmark',
    authorDate: '2026-01-02T10:00:00Z',
    message: 'Benchmark',
    subject: 'Benchmark',
    refs: [],
  }));
  await measure('graph 5000 linear', () => createCommitGraph(linear), 50);
  const wide = linear.map((commit, i) => ({
    ...commit,
    parents: i < 4900 ? [String(i + 1), String(i + 100)] : commit.parents,
  }));
  await measure(
    'graph 5000 / 100 pending lanes',
    () => createCommitGraph(wide),
    50,
  );
  const report = {
    generatedAt: new Date().toISOString(),
    environment: {
      platform: platform(),
      arch: arch(),
      cpu: cpus()[0]?.model,
      node: process.version,
      git: await fixtureGit(root, '--version'),
    },
    fixture: {
      commits: 1500,
      mode: 'warm-cache; setup excluded; local disposable repository',
    },
    memory: process.memoryUsage(),
    timings,
  };
  await mkdir('.cache', { recursive: true });
  await writeFile(
    '.cache/benchmark.json',
    JSON.stringify(report, null, 2) + '\n',
  );
  console.log(JSON.stringify(report, null, 2));
} finally {
  await rm(root, { recursive: true, force: true });
}
