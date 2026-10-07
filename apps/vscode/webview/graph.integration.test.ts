// SPDX-License-Identifier: MPL-2.0
import { it, expect } from 'vitest';
import { rm, readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { recentHistory } from '@bugsnitch/git';
import { createCommitGraph } from '@bugsnitch/graph';
import {
  createRepository,
  commitFile,
  fixtureGit,
} from '../../../packages/git/test/fixture';

it('preserves real Git merge lanes over separate bounded pages without changing the checkout', async () => {
  const root = await createRepository();
  try {
    const base = await commitFile(root, 'base.txt', 'base\n');
    await fixtureGit(root, 'checkout', '-b', 'feature');
    const feature = await commitFile(root, 'feature.txt', 'feature\n');
    await fixtureGit(root, 'checkout', 'main');
    const main = await commitFile(root, 'main.txt', 'main\n');
    await fixtureGit(
      root,
      'merge',
      '--no-ff',
      'feature',
      '-m',
      'Merge feature',
    );
    const head = await fixtureGit(root, 'rev-parse', 'HEAD');
    const before = await readFile(join(root, '.git/index'));
    const first = await recentHistory(root, { head, limit: 2 });
    const second = await recentHistory(root, { head, limit: 2, skip: 2 });
    const prefix = createCommitGraph(first.commits);
    const complete = createCommitGraph([...first.commits, ...second.commits]);
    expect(complete.nodes.slice(0, 2)).toEqual(prefix.nodes);
    expect(complete.nodes[0]?.parents).toEqual([main, feature]);
    expect(new Set(complete.nodes.map((n) => n.hash))).toEqual(
      new Set([head, main, feature, base]),
    );
    expect(complete.laneCount).toBe(2);
    expect(complete.boundaries).toEqual([]);
    expect(await fixtureGit(root, 'rev-parse', 'HEAD')).toBe(head);
    expect(await readFile(join(root, '.git/index'))).toEqual(before);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
