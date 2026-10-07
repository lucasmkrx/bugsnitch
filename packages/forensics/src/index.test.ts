// SPDX-License-Identifier: MPL-2.0
import { afterEach, expect, it } from 'vitest';
import { rm } from 'node:fs/promises';
import { commitFile, createRepository } from '../../git/test/fixture';
import { investigateLine } from './index';

const roots: string[] = [];
afterEach(async () => {
  await Promise.all(
    roots.splice(0).map((root) => rm(root, { recursive: true, force: true })),
  );
});
it('enriches line provenance with the full originating commit message', async () => {
  const root = await createRepository();
  roots.push(root);
  const hash = await commitFile(
    root,
    'app.ts',
    'first\nsecond\n',
    'Fix behavior\n\nWhy this changed.',
  );
  const investigation = await investigateLine({
    repository: root,
    path: 'app.ts',
    line: 2,
    contents: 'first\nsecond\n',
  });
  expect(investigation.commit?.hash).toBe(hash);
  expect(investigation.commit?.message).toBe(
    'Fix behavior\n\nWhy this changed.',
  );
  expect(investigation.provenance).toMatchObject({
    originalLine: 2,
    currentLine: 2,
    uncommitted: false,
  });
});
it('does not invent a commit for a local line', async () => {
  const root = await createRepository();
  roots.push(root);
  await commitFile(root, 'app.ts', 'old\n');
  const investigation = await investigateLine({
    repository: root,
    path: 'app.ts',
    line: 1,
    contents: 'local edit\n',
  });
  expect(investigation.commit).toBeNull();
  expect(investigation.provenance.uncommitted).toBe(true);
});
