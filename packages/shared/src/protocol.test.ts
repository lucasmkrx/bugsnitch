// SPDX-License-Identifier: MPL-2.0
import { expect, it } from 'vitest';
import { isHostMessage } from './host-validation';
import { parseWebviewMessage } from './protocol';
import type { HostMessage } from './protocol';

it('accepts only versioned, bounded commands and complete object IDs', () => {
  expect(parseWebviewMessage({ version: 1, type: 'ready' })).toEqual({
    version: 1,
    type: 'ready',
  });
  expect(
    parseWebviewMessage({
      version: 1,
      type: 'inspectCommit',
      hash: 'a'.repeat(40),
    }),
  ).not.toBeNull();
  expect(
    parseWebviewMessage({
      version: 1,
      type: 'focusCommit',
      hash: 'a'.repeat(40),
    }),
  ).not.toBeNull();
  expect(
    parseWebviewMessage({ version: 1, type: 'returnToHead' }),
  ).not.toBeNull();
  expect(
    parseWebviewMessage({
      version: 1,
      type: 'selectRef',
      name: 'refs/tags/v0.2.0',
    }),
  ).not.toBeNull();
  for (const value of [
    null,
    [],
    { type: 'refresh' },
    { version: 2, type: 'ready' },
    { version: 1, type: 'inspectCommit', hash: '--help' },
    { version: 1, type: 'refresh', command: 'reset' },
    { version: 1, type: 'deleteBranch' },
    { version: 1, type: 'focusCommit', hash: '--all' },
    {
      version: 1,
      type: 'focusCommit',
      hash: 'a'.repeat(40),
      path: '/elsewhere',
    },
    { version: 1, type: 'returnToHead', checkout: true },
    { version: 1, type: 'selectRef', name: '--all' },
    { version: 1, type: 'selectRef', name: 'HEAD~3' },
    { version: 1, type: 'selectRef', name: 'refs/heads/main\n--all' },
    { version: 1, type: 'selectRef', name: 'refs/heads/main', checkout: true },
  ])
    expect(parseWebviewMessage(value)).toBeNull();
});
it('rejects malformed host state before React receives it', () => {
  expect(isHostMessage({ version: 1, type: 'busy', busy: true })).toBe(true);
  expect(
    isHostMessage({
      version: 1,
      type: 'history',
      repository: {},
      page: { commits: ['fake'], hasMore: false },
      append: false,
    }),
  ).toBe(false);
  expect(
    isHostMessage({
      version: 1,
      type: 'commit',
      detail: { commit: { hash: '--help' } },
    }),
  ).toBe(false);
});

it('validates ref targets and anchors before the selector or graph receives them', () => {
  const message: HostMessage = {
    version: 1,
    type: 'history',
    repository: {
      root: '/repo',
      name: 'repo',
      branch: 'main',
      head: 'a'.repeat(40),
      filtersDisabled: false,
      status: { staged: 0, modified: 0, untracked: 0, conflicted: 0 },
    },
    page: { commits: [], hasMore: false },
    append: false,
    anchor: {
      kind: 'ref',
      hash: 'a'.repeat(40),
      label: 'tag',
      ref: 'refs/tags/tag',
    },
    returnAnchor: null,
    references: {
      references: [
        {
          name: 'refs/tags/tag',
          label: 'tag',
          kind: 'tag',
          hash: 'a'.repeat(40),
        },
      ],
      truncated: false,
    },
  };
  expect(isHostMessage(message)).toBe(true);
  expect(
    isHostMessage({ ...message, anchor: { ...message.anchor, ref: 'HEAD~3' } }),
  ).toBe(false);
  expect(
    isHostMessage({
      ...message,
      references: {
        references: [{ ...message.references.references[0], hash: '--all' }],
        truncated: false,
      },
    }),
  ).toBe(false);
  expect(
    isHostMessage({
      ...message,
      returnAnchor: { kind: 'commit', hash: null, label: 'broken' },
    }),
  ).toBe(false);
});

it('bounds evidence requests and rejects path traversal and extra capabilities', () => {
  const base = {
    version: 1,
    type: 'openDiff',
    hash: 'a'.repeat(40),
    path: 'src/λ file.ts',
  };
  expect(parseWebviewMessage(base)).not.toBeNull();
  for (const path of [
    '',
    '/etc/passwd',
    '../secrets',
    'src/../secrets',
    'nul\0path',
    '..\\secrets',
    'C:\\secrets',
    '\\server\\secrets',
    'x'.repeat(4097),
  ])
    expect(parseWebviewMessage({ ...base, path })).toBeNull();
  expect(parseWebviewMessage({ ...base, command: 'checkout' })).toBeNull();
  const annotation = {
    version: 1,
    type: 'annotate',
    hash: base.hash,
    note: 'Reproduction',
    verdict: 'confirmed',
  };
  expect(parseWebviewMessage(annotation)).not.toBeNull();
  expect(
    parseWebviewMessage({ ...annotation, note: 'x'.repeat(4001) }),
  ).toBeNull();
  expect(
    parseWebviewMessage({ ...annotation, verdict: 'automatic' }),
  ).toBeNull();
  expect(
    parseWebviewMessage({
      version: 1,
      type: 'exportInvestigation',
      destination: '/secret',
    }),
  ).toBeNull();
});
