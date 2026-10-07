// SPDX-License-Identifier: MPL-2.0
import { expect, it } from 'vitest';
import type { Commit } from '@bugsnitch/shared';
import { createCommitGraph } from './index';

function fixture(hash: string, parents: string[] = []): Commit {
  return {
    hash,
    shortHash: hash.slice(0, 7),
    parents,
    author: 'Fixture',
    authorDate: '',
    subject: hash,
    message: hash,
    refs: [],
  };
}

// Follow the drawn lines, rather than relying on their ancestry metadata.
function assertDrawnAncestry(commits: Commit[]): void {
  const graph = createCommitGraph(commits);
  graph.nodes.forEach((node) => {
    node.parents.forEach((parent, index) => {
      let lane = node.segments.filter((s) => s.from === 'node')[index]!.toLane;
      let reached: string | undefined;
      for (const next of graph.nodes.slice(node.row + 1)) {
        const line = next.segments.find(
          (s) => s.from === 'top' && s.fromLane === lane,
        );
        expect(
          line,
          `Broken line from ${node.hash} to ${parent}`,
        ).toBeDefined();
        lane = line!.toLane;
        if (line!.to === 'node') {
          reached = next.hash;
          break;
        }
      }
      expect(
        reached ?? graph.boundaries.find((b) => b.lane === lane)?.hash,
      ).toBe(parent);
    });
  });
}

it('draws a branch, merge, and shared ancestor without overlapping nodes', () => {
  const commits = [
    fixture('merge', ['main', 'feature']),
    fixture('main', ['root']),
    fixture('feature', ['root']),
    fixture('root'),
  ];
  const graph = createCommitGraph(commits);
  expect(graph.nodes.map((node) => node.lane)).toEqual([0, 0, 1, 0]);
  expect(graph.laneCount).toBe(2);
  expect(graph.boundaries).toEqual([]);
  assertDrawnAncestry(commits);
});

it('keeps every drawn row stable across pagination, including octopus and crossed merges', () => {
  const commits = [
    fixture('tip', ['left', 'right', 'third']),
    fixture('left', ['a', 'b']),
    fixture('right', ['b', 'a']),
    fixture('third', ['independent']),
    fixture('a', ['root']),
    fixture('b', ['root']),
    fixture('independent'),
    fixture('root'),
  ];
  const full = createCommitGraph(commits);
  for (let length = 1; length <= commits.length; length++) {
    const prefix = commits.slice(0, length);
    expect(createCommitGraph(prefix).nodes).toEqual(
      full.nodes.slice(0, length),
    );
    assertDrawnAncestry(prefix);
  }
});

it('reuses completed lanes and leaves missing parents as explicit continuations', () => {
  const graph = createCommitGraph([
    fixture('one'),
    fixture('two', ['unloaded']),
    fixture('three'),
  ]);
  expect(graph.nodes.map((node) => node.lane)).toEqual([0, 0, 1]);
  expect(graph.boundaries).toEqual([{ hash: 'unloaded', lane: 0 }]);
  assertDrawnAncestry([
    fixture('one'),
    fixture('two', ['unloaded']),
    fixture('three'),
  ]);
});

it('keeps long linear histories in one lane', () => {
  const commits = Array.from({ length: 5000 }, (_, i) =>
    fixture(String(i), i === 4999 ? [] : [String(i + 1)]),
  );
  const graph = createCommitGraph(commits);
  expect(graph.laneCount).toBe(1);
  expect(graph.nodes).toHaveLength(5000);
  expect(graph.boundaries).toEqual([]);
});

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

it('preserves drawn ancestry and prefix stability for deterministic generated DAGs', () => {
  let state = 0x51a7;
  const random = () => {
    state = (Math.imul(state, 1664525) + 1013904223) >>> 0;
    return state / 2 ** 32;
  };
  for (let sample = 0; sample < 30; sample++) {
    const size = 12 + Math.floor(random() * 28);
    const commits = Array.from({ length: size }, (_, row) => {
      const remaining = size - row - 1;
      const parents = new Set<string>();
      for (let i = 0; i < Math.min(remaining, Math.floor(random() * 4)); i++)
        parents.add(String(row + 1 + Math.floor(random() * remaining)));
      if (random() < 0.15) parents.add(`outside-${sample}`);
      return fixture(String(row), [...parents]);
    });
    const full = createCommitGraph(commits);
    for (let prefix = 1; prefix <= size; prefix++) {
      expect(createCommitGraph(commits.slice(0, prefix)).nodes).toEqual(
        full.nodes.slice(0, prefix),
      );
      assertDrawnAncestry(commits.slice(0, prefix));
    }
    expect(full.edges).toHaveLength(
      commits.reduce((count, commit) => count + commit.parents.length, 0),
    );
    expect(new Set(full.boundaries.map((boundary) => boundary.hash)).size).toBe(
      full.boundaries.length,
    );
  }
});
