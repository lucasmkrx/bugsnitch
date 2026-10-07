// SPDX-License-Identifier: MPL-2.0
import { afterAll, beforeAll, expect, it } from 'vitest';
import { rm } from 'node:fs/promises';
import { createRepository, commitFile } from '../../git/test/fixture';
import {
  inspectCommit,
  repositorySnapshot,
  recentHistory,
  fileHistory,
  regressionRange,
  blameLine,
} from '../../git/src/index';
import { isHostMessage } from './host-validation';
import type { InvestigationSession } from './models';
let root: string;
let good: string;
let bad: string;
beforeAll(async () => {
  root = await createRepository();
  good = await commitFile(root, 'file.ts', 'good\n');
  bad = await commitFile(root, 'file.ts', 'bad\n');
});
afterAll(async () => {
  if (root) await rm(root, { recursive: true, force: true });
});
it('accepts real Git payloads and rejects inconsistent evidence and comparisons', async () => {
  expect(
    isHostMessage({
      version: 1,
      type: 'commit',
      detail: await inspectCommit(root, bad),
    }),
  ).toBe(true);
  const detail = await inspectCommit(root, bad, { path: 'file.ts' });
  const envelope = { version: 1, type: 'commit', detail };
  expect(isHostMessage(envelope)).toBe(true);
  expect(
    isHostMessage({
      ...envelope,
      detail: { ...detail, comparisonParent: 'f'.repeat(40) },
    }),
  ).toBe(false);
  expect(
    isHostMessage({
      ...envelope,
      detail: { ...detail, selectedPath: '../outside' },
    }),
  ).toBe(false);
  expect(
    isHostMessage({
      ...envelope,
      detail: {
        ...detail,
        commit: { ...detail.commit, authorDate: 'invalid' },
      },
    }),
  ).toBe(false);
  const history = await fileHistory(root, bad, 'file.ts');
  expect(isHostMessage({ version: 1, type: 'fileHistory', history })).toBe(
    true,
  );
  expect(
    isHostMessage({
      version: 1,
      type: 'fileHistory',
      history: {
        ...history,
        commits: Array.from({ length: 101 }, () => detail.commit),
      },
    }),
  ).toBe(false);
  const range = await regressionRange(root, good, bad);
  const session: InvestigationSession = {
    good: (await inspectCommit(root, good)).commit,
    bad: detail.commit,
    range,
    annotations: [{ hash: bad, note: 'Evidence', verdict: 'confirmed' }],
  };
  expect(isHostMessage({ version: 1, type: 'session', session })).toBe(true);
  for (const invalid of [
    { ...session, good: null },
    { ...session, range: { ...range, bad: good } },
    {
      ...session,
      annotations: [{ hash: good, note: '', verdict: 'confirmed' }],
    },
    {
      ...session,
      annotations: [
        { hash: bad, note: 'x'.repeat(4001), verdict: 'confirmed' },
      ],
    },
    {
      ...session,
      annotations: [{ hash: bad, note: '', verdict: 'automatic' }],
    },
  ])
    expect(
      isHostMessage({ version: 1, type: 'session', session: invalid }),
    ).toBe(false);
  const provenance = await blameLine(root, 'file.ts', 1, 'bad\n');
  expect(
    isHostMessage({
      version: 1,
      type: 'investigation',
      investigation: { repository: root, provenance, commit: detail.commit },
    }),
  ).toBe(true);
  expect(
    isHostMessage({
      version: 1,
      type: 'investigation',
      investigation: { repository: root, provenance, commit: session.good },
    }),
  ).toBe(false);
  const uncommitted = await blameLine(root, 'file.ts', 1, 'local\n');
  expect(
    isHostMessage({
      version: 1,
      type: 'investigation',
      investigation: {
        repository: root,
        provenance: uncommitted,
        commit: null,
      },
    }),
  ).toBe(true);
  const repository = await repositorySnapshot(root);
  expect(
    isHostMessage({
      version: 1,
      type: 'history',
      repository,
      page: await recentHistory(root),
      append: false,
      anchor: { kind: 'head', hash: bad, label: 'main' },
      returnAnchor: null,
      references: { references: [], truncated: false },
    }),
  ).toBe(true);
});
