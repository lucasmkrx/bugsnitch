// SPDX-License-Identifier: MPL-2.0
import type {
  ChangedFile,
  Commit,
  LineProvenance,
  WorkingTreeStatus,
} from '@bugsnitch/shared';
import { isCommitHash } from '@bugsnitch/shared';
import { GitError } from './errors';

// Nine NUL-delimited fields and a NUL record terminator. Git commit metadata cannot contain NUL.
export const LOG_FORMAT = '%H%x00%h%x00%P%x00%an%x00%aI%x00%s%x00%B%x00%D%x00';
export function parseLog(output: string): Commit[] {
  if (!output) return [];
  const fields = output.split('\0');
  if (fields.at(-1) !== '' || (fields.length - 1) % 9 !== 0)
    throw new GitError('failed', 'Git returned an unexpected history format.');
  const commits: Commit[] = [];
  for (let offset = 0; offset < fields.length - 1; offset += 9) {
    const hash = fields[offset];
    const shortHash = fields[offset + 1];
    const parents = fields[offset + 2];
    const author = fields[offset + 3];
    const authorDate = fields[offset + 4];
    const subject = fields[offset + 5];
    const message = fields[offset + 6];
    const refs = fields[offset + 7];
    if (
      !isCommitHash(hash) ||
      [shortHash, parents, author, authorDate, subject, message, refs].some(
        (field) => field === undefined,
      ) ||
      fields[offset + 8] !== '' ||
      !/^[a-f0-9]+$/.test(shortHash ?? '') ||
      (parents !== '' && !parents?.split(' ').every(isCommitHash)) ||
      !Number.isFinite(Date.parse(authorDate ?? ''))
    ) {
      throw new GitError(
        'failed',
        'Git returned an unexpected history format.',
      );
    }
    commits.push({
      hash,
      shortHash: shortHash!,
      parents: parents ? parents.split(' ') : [],
      author: author!,
      authorDate: authorDate!,
      subject: subject!,
      message: message!.trimEnd(),
      refs: refs ? refs.split(', ') : [],
    });
  }
  return commits;
}

export function parseStatus(output: string): WorkingTreeStatus {
  const status: WorkingTreeStatus = {
    staged: 0,
    modified: 0,
    untracked: 0,
    conflicted: 0,
  };
  const records = output.split('\0');
  if (records.at(-1) !== '')
    throw new GitError('failed', 'Git returned an unexpected status format.');
  for (let index = 0; index < records.length; index++) {
    const record = records[index];
    if (!record) continue;
    if (!/^[ MADRCU?!T]{2} .+$/s.test(record))
      throw new GitError('failed', 'Git returned an unexpected status format.');
    const x = record[0];
    const y = record[1];
    if (x === '?' && y === '?') {
      status.untracked++;
      continue;
    }
    if (x === '!' && y === '!') continue;
    if (
      x === 'U' ||
      y === 'U' ||
      (x === 'A' && y === 'A') ||
      (x === 'D' && y === 'D')
    )
      status.conflicted++;
    else {
      if (x !== ' ') status.staged++;
      if (y !== ' ') status.modified++;
    }
    if (x === 'R' || x === 'C' || y === 'R' || y === 'C') {
      if (!records[++index])
        throw new GitError(
          'failed',
          'Git returned an incomplete rename status.',
        );
    }
  }
  return status;
}

// Decode Git's C-quoted paths as bytes, including UTF-8 octal escapes.
export function decodeGitPath(value: string): string {
  if (!value.startsWith('"')) return value;
  if (!value.endsWith('"'))
    throw new GitError('failed', 'Git returned an invalid quoted path.');
  const bytes: number[] = [];
  const escapes: Record<string, number> = {
    a: 7,
    b: 8,
    t: 9,
    n: 10,
    v: 11,
    f: 12,
    r: 13,
    '"': 34,
    '\\': 92,
  };
  for (let i = 1; i < value.length - 1; i++) {
    const char = value[i]!;
    if (char !== '\\') {
      bytes.push(...Buffer.from(char));
      continue;
    }
    const next = value[++i]!;
    if (i >= value.length - 1)
      throw new GitError('failed', 'Git returned an invalid quoted path.');
    if (/[0-7]/.test(next)) {
      const octal = value.slice(i, i + 3);
      if (!/^[0-7]{3}$/.test(octal) || Number.parseInt(octal, 8) > 255)
        throw new GitError('failed', 'Git returned an invalid quoted path.');
      bytes.push(Number.parseInt(octal, 8));
      i += 2;
    } else {
      if (escapes[next] === undefined)
        throw new GitError('failed', 'Git returned an invalid quoted path.');
      bytes.push(escapes[next]!);
    }
  }
  return Buffer.from(bytes).toString('utf8');
}

export function parseBlame(output: string, path: string): LineProvenance {
  const lines = output.split('\n');
  const header = /^(\w+) (\d+) (\d+)(?: \d+)?$/.exec(lines[0] ?? '');
  if (!header || !isCommitHash(header[1]))
    throw new GitError(
      'failed',
      'Git returned an unexpected line-provenance format.',
    );
  const fields = new Map<string, string>();
  for (const line of lines.slice(1)) {
    if (line.startsWith('\t')) break;
    const space = line.indexOf(' ');
    if (space > 0) fields.set(line.slice(0, space), line.slice(space + 1));
  }
  const date = Number(fields.get('author-time'));
  const originalLine = Number(header[2]);
  const currentLine = Number(header[3]);
  if (
    !fields.has('author') ||
    !fields.has('filename') ||
    !fields.has('author-time') ||
    !/^-?\d+$/.test(fields.get('author-time') ?? '') ||
    !fields.get('filename') ||
    !Number.isSafeInteger(date) ||
    !Number.isFinite(new Date(date * 1000).getTime()) ||
    !Number.isSafeInteger(originalLine) ||
    originalLine < 1 ||
    !Number.isSafeInteger(currentLine) ||
    currentLine < 1
  )
    throw new GitError('failed', 'Git returned incomplete line provenance.');
  return {
    hash: header[1],
    author: fields.get('author')!,
    authorDate: new Date(date * 1000).toISOString(),
    subject: fields.get('summary') ?? '',
    originalLine,
    currentLine,
    path,
    originalPath: decodeGitPath(fields.get('filename')!),
    uncommitted: /^0+$/.test(header[1]),
  };
}

export function parseChangedFiles(output: string): ChangedFile[] {
  if (!output) return [];
  const fields = output.split('\0');
  if (fields.at(-1) !== '' || (fields.length - 1) % 2 !== 0)
    throw new GitError(
      'failed',
      'Git returned an unexpected changed-file format.',
    );
  const files: ChangedFile[] = [];
  for (let index = 0; index < fields.length - 1; index += 2) {
    const status = fields[index];
    const path = fields[index + 1];
    if (!status || !/^[AMDTCU]$/.test(status) || !path)
      throw new GitError(
        'failed',
        'Git returned an unexpected changed-file format.',
      );
    files.push({ status, path });
  }
  return files;
}
