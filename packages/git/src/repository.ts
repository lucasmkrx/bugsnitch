// SPDX-License-Identifier: MPL-2.0
import { realpathSync } from 'node:fs';
import { basename, dirname, isAbsolute, join, relative, sep } from 'node:path';
import type {
  Commit,
  CommitDetail,
  HistoryPage,
  LineProvenance,
  RepositorySnapshot,
} from '@bugsnitch/shared';
import { isCommitHash } from '@bugsnitch/shared';
import { GitError } from './errors';
import { gitText, hasContentFilters, runGit } from './process';
import type { GitRunOptions } from './process';
import {
  LOG_FORMAT,
  parseBlame,
  parseChangedFiles,
  parseLog,
  parseStatus,
} from './parsers';

export async function findRepository(
  directory: string,
  options: GitRunOptions = {},
): Promise<string | null> {
  const result = await runGit(
    directory,
    ['rev-parse', '--show-toplevel'],
    options,
  );
  if (result.exitCode !== 0) return null;
  return result.stdout.replace(/\r?\n$/, '');
}

export function relativeGitPath(root: string, file: string): string {
  // Git canonicalizes repository roots. Canonicalize the file's parent as well,
  // retaining its basename so a tracked symlink is not mistaken for its target.
  const path = relative(
    realpathSync(root),
    join(realpathSync(dirname(file)), basename(file)),
  );
  if (
    !path ||
    isAbsolute(path) ||
    path === '..' ||
    path.startsWith('../') ||
    path.startsWith('..\\')
  ) {
    throw new GitError(
      'invalidInput',
      'The selected file is outside this repository.',
    );
  }
  return path.split(sep).join('/');
}

function validatePath(path: string): void {
  if (
    !path ||
    path.includes('\0') ||
    isAbsolute(path) ||
    path.split('/').includes('..')
  )
    throw new GitError('invalidInput', 'Choose a file inside the repository.');
}

function validateHash(hash: string): void {
  if (!isCommitHash(hash))
    throw new GitError(
      'invalidInput',
      'Choose a valid commit from Bugsnitch history.',
    );
}

export async function repositorySnapshot(
  root: string,
  options: GitRunOptions = {},
): Promise<RepositorySnapshot> {
  const [branch, head, status, filtersDisabled] = await Promise.all([
    runGit(root, ['symbolic-ref', '--quiet', '--short', 'HEAD'], options),
    runGit(root, ['rev-parse', '--verify', 'HEAD'], options),
    gitText(
      root,
      ['status', '--porcelain=v1', '-z', '--untracked-files=normal'],
      options,
    ),
    hasContentFilters(root),
  ]);
  const hash = head.exitCode === 0 ? head.stdout.trim() : null;
  if (hash !== null && !isCommitHash(hash))
    throw new GitError('failed', 'Git returned an invalid repository HEAD.');
  return {
    root,
    name: basename(root),
    branch:
      branch.exitCode === 0
        ? branch.stdout.trimEnd()
        : `Detached HEAD${hash ? ` · ${hash.slice(0, 8)}` : ''}`,
    head: hash,
    filtersDisabled,
    status: parseStatus(status),
  };
}

export interface HistoryOptions extends GitRunOptions {
  limit?: number;
  skip?: number;
  head?: string;
}
export async function recentHistory(
  root: string,
  options: HistoryOptions = {},
): Promise<HistoryPage> {
  const limit = options.limit ?? 50;
  const skip = options.skip ?? 0;
  if (
    !Number.isSafeInteger(limit) ||
    limit < 1 ||
    limit > 200 ||
    !Number.isSafeInteger(skip) ||
    skip < 0
  )
    throw new GitError(
      'invalidInput',
      'History pagination is outside the supported range.',
    );
  if (options.head) validateHash(options.head);
  else {
    const head = await runGit(root, ['rev-parse', '--verify', 'HEAD'], options);
    if (head.exitCode !== 0) return { commits: [], hasMore: false };
  }
  const output = await gitText(
    root,
    [
      'log',
      '-z',
      '--topo-order',
      '--decorate=short',
      `--format=${LOG_FORMAT}`,
      `--max-count=${limit + 1}`,
      `--skip=${skip}`,
      options.head ?? 'HEAD',
      '--',
    ],
    options,
  );
  const commits = parseLog(output);
  return { commits: commits.slice(0, limit), hasMore: commits.length > limit };
}

export async function readCommit(
  root: string,
  hash: string,
  options: GitRunOptions = {},
): Promise<Commit> {
  validateHash(hash);
  const output = await gitText(
    root,
    [
      'log',
      '-1',
      '-z',
      '--no-walk',
      '--decorate=short',
      `--format=${LOG_FORMAT}`,
      hash,
      '--',
    ],
    options,
  );
  const commit = parseLog(output)[0];
  if (!commit)
    throw new GitError(
      'failed',
      'This commit is no longer available in the repository. Refresh history.',
    );
  return commit;
}

export async function inspectCommit(
  root: string,
  hash: string,
  options: GitRunOptions = {},
): Promise<CommitDetail> {
  validateHash(hash);
  const commit = await readCommit(root, hash, options);
  const revisions = commit.parents[0]
    ? [commit.parents[0], hash]
    : ['--root', hash];
  const [files, diff] = await Promise.all([
    gitText(
      root,
      [
        'diff-tree',
        '--no-commit-id',
        '--name-status',
        '-r',
        '-z',
        '--no-renames',
        '--no-ext-diff',
        '--no-textconv',
        ...revisions,
        '--',
      ],
      options,
    ),
    runGit(
      root,
      [
        'show',
        '--format=',
        '--first-parent',
        '--no-ext-diff',
        '--no-textconv',
        '--no-renames',
        '--no-color',
        '--unified=3',
        hash,
        '--',
      ],
      { ...options, maxBytes: 256 * 1024, truncate: true },
    ),
  ]);
  if (diff.exitCode !== 0 && !diff.truncated)
    throw new GitError(
      'failed',
      'Git could not read the changes in this commit.',
    );
  return {
    commit,
    files: parseChangedFiles(files),
    diff: diff.stdout,
    diffTruncated: diff.truncated,
  };
}

export async function blameLine(
  root: string,
  path: string,
  line: number,
  contents: string,
  options: GitRunOptions = {},
): Promise<LineProvenance> {
  validatePath(path);
  if (!Number.isSafeInteger(line) || line < 1)
    throw new GitError('invalidInput', 'Choose a valid line in the editor.');
  if (Buffer.byteLength(contents) > 4 * 1024 * 1024)
    throw new GitError(
      'tooLarge',
      'Line investigation supports text files up to 4 MiB.',
    );
  if (contents.includes('\0'))
    throw new GitError(
      'binary',
      'Line investigation requires a text file. This file appears to be binary.',
    );
  const head = await runGit(root, ['rev-parse', '--verify', 'HEAD'], options);
  if (head.exitCode !== 0)
    throw new GitError(
      'emptyRepository',
      'This repository has no commits yet. Line provenance starts with the first commit.',
    );
  const tracked = await runGit(
    root,
    ['ls-files', '--error-unmatch', '--', path],
    options,
  );
  if (tracked.exitCode !== 0)
    throw new GitError(
      'untracked',
      'This file is not tracked by Git. Commit it before investigating its history.',
    );
  const attributes = await gitText(
    root,
    ['check-attr', '-z', 'filter', '--', path],
    options,
  );
  const driver = attributes.split('\0')[2];
  if (driver && driver !== 'unspecified' && driver !== 'unset')
    throw new GitError(
      'failed',
      'This file uses a Git content filter. Line tracing is unavailable because Bugsnitch does not execute filter helpers. Commit inspection remains available.',
    );
  const output = await runGit(
    root,
    [
      'blame',
      '--line-porcelain',
      '--contents',
      '-',
      '-L',
      `${line},${line}`,
      '--',
      path,
    ],
    { ...options, input: contents },
  );
  if (output.exitCode !== 0)
    throw new GitError(
      'failed',
      'Git could not trace this line. It may be newly added, renamed before its first commit, deleted, or outside the saved file. Save the file and try again.',
    );
  return parseBlame(output.stdout, path);
}
