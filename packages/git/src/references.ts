// SPDX-License-Identifier: MPL-2.0
import type { ReferencePage, RepositoryReference } from '@bugsnitch/shared';
import { isCommitHash, isReferenceName } from '@bugsnitch/shared';
import { GitError } from './errors';
import { gitText } from './process';
import type { GitRunOptions } from './process';

/** Read local refs and peel tags in one batch, without fetching or checking out. */
export async function listReferences(
  root: string,
  options: GitRunOptions & { limit?: number } = {},
): Promise<ReferencePage> {
  const limit = options.limit ?? 2000;
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 2000)
    throw new GitError(
      'invalidInput',
      'Reference listing is outside the supported range.',
    );
  const output = await gitText(
    root,
    [
      'for-each-ref',
      `--count=${limit + 1}`,
      '--sort=refname',
      '--format=%(refname)',
      'refs/heads/',
      'refs/remotes/',
      'refs/tags/',
    ],
    options,
  );
  const names = output.split('\n').filter(Boolean);
  if (!names.every(isReferenceName))
    throw new GitError('failed', 'Git returned an unexpected reference name.');
  const selected = names.slice(0, limit);
  if (!selected.length) return { references: [], truncated: false };
  // ^{commit} recursively peels annotated tags and rejects tags to blobs/trees.
  // Ref names cannot contain line breaks; they are stdin data, never commands.
  const resolved = await gitText(
    root,
    ['cat-file', '--batch-check=%(objectname) %(objecttype)'],
    {
      ...options,
      input: selected.map((name) => `${name}^{commit}\n`).join(''),
    },
  );
  const records = resolved.trimEnd().split('\n');
  if (records.length !== selected.length)
    throw new GitError(
      'failed',
      'Git returned an unexpected reference listing.',
    );
  const references = selected.flatMap((name, index): RepositoryReference[] => {
    const record = records[index]!;
    if (record.endsWith(' missing')) return [];
    const [hash, type] = record.split(' ');
    if (type !== 'commit' || !isCommitHash(hash))
      throw new GitError('failed', 'Git returned an invalid reference target.');
    const kind = name.startsWith('refs/heads/')
      ? 'branch'
      : name.startsWith('refs/remotes/')
        ? 'remote'
        : 'tag';
    return [
      {
        name,
        hash,
        kind,
        label: name.replace(/^refs\/(?:heads|remotes|tags)\//u, ''),
      },
    ];
  });
  return { references, truncated: names.length > limit };
}
