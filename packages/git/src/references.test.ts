// SPDX-License-Identifier: MPL-2.0
import { it, expect } from 'vitest';
import { readFile, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { createRepository, commitFile, fixtureGit } from '../test/fixture';
import { listReferences } from './references';

it('peels lightweight, annotated, and nested tags while excluding non-commit tags, without altering local state', async () => {
  const root = await createRepository();
  try {
    const base = await commitFile(root, 'code.ts', 'base\n');
    const head = await commitFile(root, 'code.ts', 'new\n');
    await fixtureGit(root, 'branch', 'topic/山', base);
    await fixtureGit(root, 'tag', 'light', base);
    await fixtureGit(
      root,
      'tag',
      '-a',
      'annotated',
      '-m',
      'Annotated tag',
      head,
    );
    await fixtureGit(
      root,
      'tag',
      '-a',
      'nested',
      '-m',
      'Nested tag',
      'annotated',
    );
    const blob = await fixtureGit(root, 'rev-parse', 'HEAD:code.ts');
    await fixtureGit(root, 'tag', 'blob', blob);
    await fixtureGit(
      root,
      'tag',
      '-a',
      'annotated-blob',
      '-m',
      'Not a commit',
      blob,
    );
    await fixtureGit(root, 'update-ref', 'refs/remotes/origin/main', base);
    await fixtureGit(
      root,
      'symbolic-ref',
      'refs/remotes/origin/HEAD',
      'refs/remotes/origin/main',
    );
    await writeFile(join(root, 'code.ts'), 'local changes\n');
    await writeFile(join(root, 'untracked.txt'), 'untracked\n');
    const before = await readFile(join(root, '.git/index'));
    const page = await listReferences(root);
    const refs = Object.fromEntries(
      page.references.map((ref) => [ref.name, ref]),
    );
    expect(refs['refs/heads/topic/山']).toMatchObject({
      kind: 'branch',
      hash: base,
      label: 'topic/山',
    });
    expect(refs['refs/remotes/origin/HEAD']).toMatchObject({
      kind: 'remote',
      hash: base,
    });
    expect(refs['refs/tags/light']?.hash).toBe(base);
    expect(refs['refs/tags/annotated']?.hash).toBe(head);
    expect(refs['refs/tags/nested']?.hash).toBe(head);
    expect(refs['refs/tags/blob']).toBeUndefined();
    expect(refs['refs/tags/annotated-blob']).toBeUndefined();
    expect(page.truncated).toBe(false);
    expect((await listReferences(root, { limit: 1 })).truncated).toBe(true);
    await expect(listReferences(root, { limit: 0 })).rejects.toMatchObject({
      code: 'invalidInput',
    });
    expect(await fixtureGit(root, 'rev-parse', 'HEAD')).toBe(head);
    expect(await readFile(join(root, '.git/index'))).toEqual(before);
    expect(await readFile(join(root, 'code.ts'), 'utf8')).toBe(
      'local changes\n',
    );
    expect(await readFile(join(root, 'untracked.txt'), 'utf8')).toBe(
      'untracked\n',
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

it('lists an empty repository and exposes shallow clone ancestry only from local objects', async () => {
  const source = await createRepository();
  const clone = await createRepository();
  try {
    expect(await listReferences(source)).toEqual({
      references: [],
      truncated: false,
    });
    await commitFile(source, 'code.ts', 'first\n');
    const head = await commitFile(source, 'code.ts', 'second\n');
    await rm(clone, { recursive: true, force: true });
    await fixtureGit(
      source,
      'clone',
      '--depth=1',
      pathToFileURL(source).toString(),
      clone,
    );
    expect(
      (await listReferences(clone)).references.find(
        (ref) => ref.name === 'refs/heads/main',
      )?.hash,
    ).toBe(head);
  } finally {
    await Promise.all(
      [source, clone].map((root) => rm(root, { recursive: true, force: true })),
    );
  }
});
