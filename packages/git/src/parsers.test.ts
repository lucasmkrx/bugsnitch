// SPDX-License-Identifier: MPL-2.0
import { expect, it } from 'vitest';
import {
  decodeGitPath,
  parseBlame,
  parseChangedFiles,
  parseLog,
  parseStatus,
} from './parsers';

it('rejects malformed log and blame data', () => {
  expect(() => parseLog('not a log\0')).toThrow('unexpected history');
  expect(() => parseBlame('not blame', 'file')).toThrow('unexpected line');
});
it('rejects incomplete records and invalid blame coordinates and dates', () => {
  for (const input of ['malformed log', 'x\0', '\0'])
    expect(() => parseLog(input)).toThrow('unexpected history');
  for (const input of ['garbage\0', 'M  path', 'R  target\0'])
    expect(() => parseStatus(input)).toThrow();
  for (const input of ['M\0', 'M\0\0', 'BAD\0file\0'])
    expect(() => parseChangedFiles(input)).toThrow();
  const hash = 'a'.repeat(40);
  for (const [line, date] of [
    ['0', '1'],
    ['1', '9000000000000000'],
    ['1', ''],
  ]) {
    expect(() =>
      parseBlame(
        `${hash} ${line} 1\nauthor A\nauthor-time ${date}\nfilename file\n\tx`,
        'file',
      ),
    ).toThrow();
  }
  for (const input of ['"unfinished', '"bad\\q"', '"bad\\777"'])
    expect(() => decodeGitPath(input)).toThrow();
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
