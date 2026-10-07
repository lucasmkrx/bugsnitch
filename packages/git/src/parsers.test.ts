// SPDX-License-Identifier: MPL-2.0
import { expect, it } from 'vitest';
import { decodeGitPath, parseBlame, parseLog, parseStatus } from './parsers';

it('rejects malformed log and blame data', () => {
  expect(() => parseLog('not a log\0')).toThrow('unexpected history');
  expect(() => parseBlame('not blame', 'file')).toThrow('unexpected line');
});
it('decodes tabs, quotes, newlines and UTF-8 octal paths', () => {
  expect(decodeGitPath('"a\\t\\"\\n\\345\\261\\261.txt"')).toBe('a\t"\n山.txt');
});
it('counts conflicts without treating them as normal staged changes', () => {
  expect(parseStatus('UU conflict\0?? new\0')).toEqual({
    staged: 0,
    modified: 0,
    untracked: 1,
    conflicted: 1,
  });
});
