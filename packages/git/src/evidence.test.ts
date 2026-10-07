// SPDX-License-Identifier: MPL-2.0
import { afterEach, expect, it } from 'vitest';
import { readFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { commitFile, createRepository, fixtureGit } from '../test/fixture';
import { fileHistory, historicalFile, regressionRange } from './evidence';
import { inspectCommit } from './repository';
const roots: string[] = [];
async function repo() {
  const root = await createRepository();
  roots.push(root);
  return root;
}
afterEach(async () => {
  await Promise.all(
    roots.splice(0).map((root) => rm(root, { recursive: true, force: true })),
  );
});

it('reads historical additions, changes, deletions and literal Unicode paths without checkout', async () => {
  const root = await repo();
  const path = 'literal [x] 山.txt';
  const good = await commitFile(root, path, 'good\n');
  const bad = await commitFile(root, path, 'bad\n');
  const index = await readFile(join(root, '.git/index'));
  expect(await historicalFile(root, good, path)).toBe('good\n');
  expect(await historicalFile(root, bad, path)).toBe('bad\n');
  expect(await historicalFile(root, good, 'absent.txt')).toBe('');
  const detail = await inspectCommit(root, bad, { path });
  expect(detail.selectedPath).toBe(path);
  expect(detail.diff).toContain('+bad');
  await expect(
    inspectCommit(root, bad, { parent: 'a'.repeat(40) }),
  ).rejects.toMatchObject({ code: 'invalidInput' });
  expect(await readFile(join(root, '.git/index'))).toEqual(index);
  await fixtureGit(root, 'rm', '--', path);
  await fixtureGit(root, 'commit', '-m', 'Delete file');
  expect(
    await historicalFile(
      root,
      await fixtureGit(root, 'rev-parse', 'HEAD'),
      path,
    ),
  ).toBe('');
});

it('follows file renames and rejects binary and traversal requests', async () => {
  const root = await repo();
  const first = await commitFile(root, 'old.txt', 'text\n');
  await fixtureGit(root, 'mv', 'old.txt', 'new.txt');
  await fixtureGit(root, 'commit', '-m', 'Rename file');
  const tip = await fixtureGit(root, 'rev-parse', 'HEAD');
  expect((await fileHistory(root, tip, 'new.txt')).commits.at(-1)?.hash).toBe(
    first,
  );
  const binary = await commitFile(root, 'binary.dat', '\0data');
  await expect(
    historicalFile(root, binary, 'binary.dat'),
  ).rejects.toMatchObject({ code: 'binary' });
  await expect(
    historicalFile(root, binary, '../outside'),
  ).rejects.toMatchObject({ code: 'invalidInput' });
});

it('freezes ancestor ranges, preserves merge candidates and refuses diverged or shallow history', async () => {
  const root = await repo();
  const good = await commitFile(root, 'file', 'good\n');
  await fixtureGit(root, 'checkout', '-b', 'feature');
  const change = await commitFile(root, 'file', 'bad\n');
  await fixtureGit(root, 'checkout', 'main');
  await commitFile(root, 'docs', 'documentation\n');
  await fixtureGit(root, 'merge', '--no-ff', 'feature', '-m', 'Merge change');
  const bad = await fixtureGit(root, 'rev-parse', 'HEAD');
  const range = await regressionRange(root, good, bad);
  expect(range.commits.map((commit) => commit.hash)).toContain(change);
  expect(range.commits.map((commit) => commit.hash)).not.toContain(good);
  await commitFile(root, 'new', 'later\n');
  expect(await regressionRange(root, good, bad)).toEqual(range);
  await expect(regressionRange(root, bad, good)).rejects.toMatchObject({
    code: 'invalidInput',
  });
  await expect(regressionRange(root, good, good)).rejects.toMatchObject({
    code: 'invalidInput',
  });
  const clone = await repo();
  await rm(clone, { recursive: true, force: true });
  await fixtureGit(
    root,
    'clone',
    '--depth=1',
    new URL(`file://${root}`).href,
    clone,
  );
  await expect(regressionRange(clone, good, bad)).rejects.toThrow('shallow');
});
