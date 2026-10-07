// SPDX-License-Identifier: MPL-2.0
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdtemp, realpath, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const execute = promisify(execFile);
export async function fixtureGit(
  root: string,
  ...args: string[]
): Promise<string> {
  const result = await execute('git', ['-C', root, ...args], {
    env: {
      ...process.env,
      GIT_CONFIG_NOSYSTEM: '1',
      GIT_CONFIG_GLOBAL: process.platform === 'win32' ? 'NUL' : '/dev/null',
      GIT_AUTHOR_DATE: '2026-01-02T10:00:00Z',
      GIT_COMMITTER_DATE: '2026-01-02T10:00:00Z',
    },
  });
  return result.stdout.trimEnd();
}
export async function createRepository(): Promise<string> {
  const root = await realpath(await mkdtemp(join(tmpdir(), 'bugsnitch test ')));
  await fixtureGit(root, 'init', '-b', 'main');
  await fixtureGit(root, 'config', 'user.name', 'Lúcia 山田');
  await fixtureGit(root, 'config', 'user.email', 'fixture@example.invalid');
  await fixtureGit(root, 'config', 'commit.gpgsign', 'false');
  await fixtureGit(root, 'config', 'core.autocrlf', 'false');
  return root;
}
export async function commitFile(
  root: string,
  path: string,
  contents: string,
  message = 'Start the trail',
): Promise<string> {
  await writeFile(join(root, path), contents);
  await fixtureGit(root, 'add', '--', path);
  await fixtureGit(root, 'commit', '-m', message);
  return fixtureGit(root, 'rev-parse', 'HEAD');
}
