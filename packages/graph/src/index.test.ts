// SPDX-License-Identifier: MPL-2.0
import { expect, it } from 'vitest';
import type { Commit } from '@bugsnitch/shared';
import { createCommitGraph } from './index';

it('preserves merge edges and marks parents outside the loaded page', () => {
  const commit: Commit = {
    hash: 'a'.repeat(40),
    shortHash: 'aaaaaaa',
    parents: ['b'.repeat(40), 'c'.repeat(40)],
    author: 'Fixture',
    authorDate: '',
    subject: 'Merge',
    message: 'Merge',
    refs: ['main'],
  };
  const graph = createCommitGraph([
    commit,
    { ...commit, hash: 'b'.repeat(40), parents: [] },
  ]);
  expect(graph.edges).toEqual([
    { from: commit.hash, to: 'b'.repeat(40), outsidePage: false },
    { from: commit.hash, to: 'c'.repeat(40), outsidePage: true },
  ]);
  expect(graph.nodes[0]?.row).toBe(0);
});
