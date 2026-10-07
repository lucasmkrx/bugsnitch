// SPDX-License-Identifier: MPL-2.0
import { expect, it } from 'vitest';
import { isHostMessage } from './host-validation';
import { parseWebviewMessage } from './protocol';

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
