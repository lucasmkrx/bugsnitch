// SPDX-License-Identifier: MPL-2.0
import type { FileHistory, RegressionRange } from '@bugsnitch/shared';
import { GitError } from './errors';
import { gitText, runGit } from './process';
import type { GitRunOptions } from './process';
import { LOG_FORMAT, parseLog } from './parsers';
import { validateHash, validatePath } from './repository';

/** Read a local blob without executing filters, checkout, or text conversion. */
export async function historicalFile(
  root: string,
  hash: string,
  path: string,
  options: GitRunOptions = {},
): Promise<string> {
  validateHash(hash);
  validatePath(path);
  const tree = await gitText(
    root,
    ['ls-tree', '-z', '--format=%(objecttype) %(objectname)', hash, '--', path],
    options,
  );
  if (!tree) return ''; // An addition/deletion has an empty side in the diff editor.
  const records = tree.split('\0').filter(Boolean);
  const [type, object] = records[0]!.split(' ');
  if (records.length !== 1 || type !== 'blob' || !object)
    throw new GitError(
      'binary',
      'This entry is a directory or submodule. Inspect its gitlink patch instead.',
    );
  validateHash(object);
  const contents = await gitText(root, ['cat-file', 'blob', object], {
    ...options,
    maxBytes: 4 * 1024 * 1024,
  });
  if (contents.includes('\0'))
    throw new GitError(
      'binary',
      'This historical file is binary. Its metadata remains available.',
    );
  return contents;
}

/** A bounded local file history, following renames along Git's chosen history. */
export async function fileHistory(
  root: string,
  tip: string,
  path: string,
  options: GitRunOptions = {},
): Promise<FileHistory> {
  validateHash(tip);
  validatePath(path);
  const output = await gitText(
    root,
    [
      'log',
      '-z',
      '--follow',
      '--topo-order',
      '--decorate=short',
      `--format=${LOG_FORMAT}`,
      '--max-count=101',
      tip,
      '--',
      path,
    ],
    options,
  );
  const commits = parseLog(output);
  return {
    path,
    tip,
    commits: commits.slice(0, 100),
    hasMore: commits.length > 100,
  };
}

/** Requires an ancestor range; it never treats divergent tips as a regression interval. */
export async function regressionRange(
  root: string,
  good: string,
  bad: string,
  options: GitRunOptions = {},
): Promise<RegressionRange> {
  validateHash(good);
  validateHash(bad);
  if (good === bad)
    throw new GitError(
      'invalidInput',
      'Choose different known-good and known-bad commits.',
    );
  const shallow = await gitText(
    root,
    ['rev-parse', '--is-shallow-repository'],
    options,
  );
  if (shallow.trim() === 'true')
    throw new GitError(
      'failed',
      'Regression ranges require complete local ancestry. This clone is shallow; use commit inspection instead.',
    );
  const ancestry = await runGit(
    root,
    ['merge-base', '--is-ancestor', good, bad],
    options,
  );
  if (ancestry.exitCode !== 0)
    throw new GitError(
      'invalidInput',
      'The known-good commit must be an ancestor of the known-bad commit. Diverged or incomplete histories cannot define this range.',
    );
  const output = await gitText(
    root,
    [
      'log',
      '-z',
      '--topo-order',
      '--decorate=short',
      `--format=${LOG_FORMAT}`,
      '--max-count=201',
      `${good}..${bad}`,
      '--',
    ],
    options,
  );
  const commits = parseLog(output);
  return {
    good,
    bad,
    commits: commits.slice(0, 200).map((commit) => ({ ...commit, refs: [] })),
    hasMore: commits.length > 200,
  };
}
