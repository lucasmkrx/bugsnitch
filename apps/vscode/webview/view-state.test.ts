// SPDX-License-Identifier: MPL-2.0
import { expect, it } from 'vitest';
import type { Commit, HostMessage, Investigation } from '@bugsnitch/shared';
import { initialViewState, reduceViewState } from './view-state';

const hash = 'a'.repeat(40);
const commit: Commit = {
  hash,
  shortHash: 'aaaaaaa',
  parents: ['b'.repeat(40)],
  author: 'Fixture',
  authorDate: '',
  subject: 'A lead',
  message: 'A lead',
  refs: [],
};
const investigation: Investigation = {
  repository: '/repo',
  commit,
  provenance: {
    hash,
    author: 'Fixture',
    authorDate: '',
    subject: 'A lead',
    originalLine: 1,
    currentLine: 2,
    path: 'code.ts',
    originalPath: 'code.ts',
    uncommitted: false,
  },
};
const history = (
  append: boolean,
  commits: Commit[],
): Extract<HostMessage, { type: 'history' }> => ({
  version: 1,
  type: 'history',
  repository: {
    root: '/repo',
    name: 'repo',
    branch: 'main',
    head: hash,
    filtersDisabled: false,
    status: { staged: 0, modified: 0, untracked: 0, conflicted: 0 },
  },
  anchor: { kind: 'head', hash, label: 'main' },
  returnAnchor: null,
  references: { references: [], truncated: false },
  page: { commits, hasMore: true },
  append,
});
const receive = (state = initialViewState, message: HostMessage) =>
  reduceViewState(state, { type: 'host', message });

it('preserves selected commit, detail, and line evidence through refresh and pagination', () => {
  let state = receive(initialViewState, {
    version: 1,
    type: 'investigation',
    investigation,
  });
  state = receive(state, {
    version: 1,
    type: 'commit',
    detail: { commit, files: [], diff: 'patch', diffTruncated: false },
  });
  const detail = state.detail;
  state = receive(state, history(false, []));
  expect(state.selectedHash).toBe(hash);
  expect(state.detail).toBe(detail);
  expect(state.investigation).toBe(investigation);
  state = receive(state, {
    ...history(false, []),
    anchor: {
      kind: 'ref',
      hash: 'b'.repeat(40),
      label: 'other',
      ref: 'refs/heads/other',
    },
  });
  expect(state.selectedHash).toBe(hash);
  expect(state.detail).toBe(detail);
  expect(state.investigation).toBe(investigation);
  state = receive(state, history(true, [commit]));
  expect(state.commits).toEqual([commit]);
  expect(state.selectedHash).toBe(hash);
  expect(state.detail).toBe(detail);
});

it('retains line context when inspecting a parent and clears only an unsuccessful reveal', () => {
  let state = receive(initialViewState, {
    version: 1,
    type: 'investigation',
    investigation,
  });
  state = reduceViewState(state, { type: 'select', hash: commit.parents[0]! });
  state = reduceViewState(state, { type: 'reveal', hash: commit.parents[0]! });
  expect(state.selectedHash).toBe(commit.parents[0]);
  expect(state.investigation).toBe(investigation);
  state = receive(state, {
    version: 1,
    type: 'error',
    message: 'Missing local object',
  });
  expect(state.revealHash).toBeNull();
  expect(state.investigation).toBe(investigation);
  expect(state.selectedHash).toBe(commit.parents[0]);
});

it('does not attribute uncommitted evidence to a historical commit', () => {
  const state = receive(initialViewState, {
    version: 1,
    type: 'investigation',
    investigation: {
      ...investigation,
      commit: null,
      provenance: {
        ...investigation.provenance,
        uncommitted: true,
        hash: '0'.repeat(40),
      },
    },
  });
  expect(state.selectedHash).toBeNull();
});
