// SPDX-License-Identifier: MPL-2.0
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdtemp, realpath, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const execute = promisify(execFile);
const environment = Object.fromEntries(
  Object.entries(process.env).filter(
    ([key]) => !key.toUpperCase().startsWith('GIT_'),
  ),
);
Object.assign(environment, {
  GIT_CONFIG_NOSYSTEM: '1',
  GIT_CONFIG_GLOBAL: process.platform === 'win32' ? 'NUL' : '/dev/null',
  GIT_TERMINAL_PROMPT: '0',
  GIT_AUTHOR_DATE: '2026-01-02T10:00:00Z',
  GIT_COMMITTER_DATE: '2026-01-02T10:00:00Z',
});

export async function fixtureGit(root, ...args) {
  const { stdout } = await execute('git', ['-C', root, ...args], {
    env: environment,
    maxBuffer: 8 * 1024 * 1024,
  });
  return stdout.trimEnd();
}

// Only create and mutate a fresh temporary repository, never a caller's checkout.
export async function createDemoFixture() {
  const root = await realpath(await mkdtemp(join(tmpdir(), 'bugsnitch demo ')));
  await fixtureGit(root, 'init', '-b', 'main');
  for (const [key, value] of [
    ['user.name', 'Demo Developer'],
    ['user.email', 'demo@example.invalid'],
    ['commit.gpgsign', 'false'],
    ['core.autocrlf', 'false'],
  ])
    await fixtureGit(root, 'config', key, value);
  const correct =
    'export function total(items) {\n  return items.reduce((sum, item) => sum + item.price * item.quantity, 0);\n}\n';
  const changed =
    'export function total(items) {\n  return items.reduce((sum, item) => sum + item.price, 0);\n}\n';
  await writeFile(join(root, 'checkout.js'), correct);
  await fixtureGit(root, 'add', '--', 'checkout.js');
  await fixtureGit(
    root,
    'commit',
    '-m',
    'Respect item quantity when calculating totals',
  );
  const good = await fixtureGit(root, 'rev-parse', 'HEAD');
  await fixtureGit(root, 'tag', '-a', 'known-good', '-m', 'Demo baseline');
  await fixtureGit(root, 'checkout', '-b', 'feature/faster-totals');
  await writeFile(join(root, 'checkout.js'), changed);
  await fixtureGit(root, 'add', '--', 'checkout.js');
  await fixtureGit(root, 'commit', '-m', 'Simplify total calculation');
  const suspect = await fixtureGit(root, 'rev-parse', 'HEAD');
  await fixtureGit(root, 'checkout', 'main');
  for (let i = 1; i <= 12; i++) {
    await writeFile(
      join(root, 'README.md'),
      `# Demo checkout\n\nDocumentation revision ${i}.\n`,
    );
    await fixtureGit(root, 'add', '--', 'README.md');
    await fixtureGit(root, 'commit', '-m', `Document checkout behavior ${i}`);
  }
  await fixtureGit(
    root,
    'merge',
    '--no-ff',
    'feature/faster-totals',
    '-m',
    'Merge faster total calculation',
  );
  const head = await fixtureGit(root, 'rev-parse', 'HEAD');
  await fixtureGit(
    root,
    'tag',
    '-a',
    'investigate',
    '-m',
    'Demo investigation entry point',
  );
  return { root, good, suspect, head };
}
