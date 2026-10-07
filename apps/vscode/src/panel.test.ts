// SPDX-License-Identifier: MPL-2.0
import { expect, it, vi } from 'vitest';
import { readFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import type { ExtensionContext } from 'vscode';
import type { HostMessage, WebviewMessage } from '@bugsnitch/shared';
import {
  createRepository,
  commitFile,
  fixtureGit,
} from '../../../packages/git/test/fixture';

const mock = vi.hoisted(() => ({
  messages: [] as HostMessage[],
  destination: undefined as { path: string } | undefined,
  writes: [] as { path: string; contents: string }[],
  clipboard: [] as string[],
  receive: undefined as ((value: unknown) => void) | undefined,
  dispose: undefined as (() => void) | undefined,
}));
vi.mock('vscode', () => ({
  Uri: {
    joinPath: (base: { path: string }, ...paths: string[]) => ({
      path: [base.path, ...paths].join('/'),
    }),
  },
  ViewColumn: { Beside: 2 },
  env: {
    clipboard: {
      writeText: async (text: string) => {
        mock.clipboard.push(text);
      },
    },
  },
  workspace: {
    getConfiguration: () => ({ get: () => 10 }),
    fs: {
      writeFile: async (uri: { path: string }, contents: Uint8Array) => {
        mock.writes.push({
          path: uri.path,
          contents: Buffer.from(contents).toString('utf8'),
        });
      },
    },
  },
  window: {
    showSaveDialog: async () => mock.destination,
    createWebviewPanel: () => ({
      webview: {
        cspSource: 'local:',
        html: '',
        asWebviewUri: (uri: { path: string }) => ({ toString: () => uri.path }),
        postMessage: async (message: HostMessage) => {
          mock.messages.push(message);
          return true;
        },
        onDidReceiveMessage: (handler: (value: unknown) => void) => {
          mock.receive = handler;
          return { dispose() {} };
        },
      },
      reveal() {},
      dispose() {
        mock.dispose?.();
      },
      onDidDispose: (handler: () => void) => {
        mock.dispose = handler;
        return { dispose() {} };
      },
    }),
  },
}));
import { BugsnitchPanel } from './panel';

async function request(message: WebviewMessage) {
  const before = mock.messages.length;
  mock.receive?.(message);
  await vi.waitFor(
    () => {
      expect(mock.messages.length).toBeGreaterThan(before);
      expect(mock.messages.at(-1)).toEqual({
        version: 1,
        type: 'busy',
        busy: false,
      });
    },
    { timeout: 10000 },
  );
}
function lastHistory() {
  const message = [...mock.messages]
    .reverse()
    .find((m) => m.type === 'history');
  if (message?.type !== 'history') throw new Error('No history received');
  return message;
}

it('allows graph focus and parent navigation only for host-supplied commits and preserves checkout/index', async () => {
  const root = await createRepository();
  mock.messages = [];
  let panel: BugsnitchPanel | undefined;
  try {
    for (let i = 0; i < 13; i++)
      await commitFile(
        root,
        'code.ts',
        `export const value = ${i};\n`,
        `Change ${i}`,
      );
    const head = await fixtureGit(root, 'rev-parse', 'HEAD');
    const parentHead = await fixtureGit(root, 'rev-parse', 'HEAD^');
    await fixtureGit(root, 'branch', 'feature/山', head);
    await fixtureGit(
      root,
      'tag',
      '-a',
      'v0.2.0',
      '-m',
      'Fixture tag',
      parentHead,
    );
    const index = await readFile(join(root, '.git/index'));
    panel = new BugsnitchPanel(
      {
        extensionUri: { path: 'file:///extension' },
      } as unknown as ExtensionContext,
      root,
      () => {},
    );
    await request({ version: 1, type: 'ready' });
    await request({ version: 1, type: 'loadMore' });
    const loadedHashes = mock.messages
      .filter((message) => message.type === 'history')
      .flatMap((message) => message.page.commits.map((commit) => commit.hash));
    await request({ version: 1, type: 'ready' });
    expect(lastHistory().page.commits.map((commit) => commit.hash)).toEqual(
      loadedHashes,
    );
    // A ready handshake during an active read is deferred, never dropped.
    const refreshing = request({ version: 1, type: 'refresh' });
    mock.receive?.({ version: 1, type: 'ready' });
    await refreshing;
    expect(lastHistory().append).toBe(false);
    expect(lastHistory().page.commits).toHaveLength(10);
    const boundary = lastHistory().page.commits.at(-1)!;
    const parent = boundary.parents[0]!;
    // A new editor investigation arriving during a read wins over its older
    // inspection response, instead of disappearing behind that response.
    const inspecting = request({
      version: 1,
      type: 'inspectCommit',
      hash: head,
    });
    await panel.showInvestigation({
      repository: root,
      commit: boundary,
      provenance: {
        hash: boundary.hash,
        author: boundary.author,
        authorDate: boundary.authorDate,
        subject: boundary.subject,
        originalLine: 1,
        currentLine: 1,
        path: 'code.ts',
        originalPath: 'code.ts',
        uncommitted: false,
      },
    });
    await inspecting;
    expect(mock.messages.at(-2)).toMatchObject({
      type: 'investigation',
      investigation: { commit: { hash: boundary.hash } },
    });
    const before = mock.messages.length;
    mock.receive?.({ version: 1, type: 'focusCommit', hash: parent });
    expect(mock.messages).toHaveLength(before);
    await request({ version: 1, type: 'inspectCommit', hash: boundary.hash });
    await request({ version: 1, type: 'inspectCommit', hash: parent });
    await request({ version: 1, type: 'focusCommit', hash: parent });
    expect(lastHistory().anchor).toMatchObject({
      kind: 'commit',
      hash: parent,
    });
    expect(lastHistory().page.commits[0]?.hash).toBe(parent);
    await request({
      version: 1,
      type: 'selectRef',
      name: 'refs/heads/feature/山',
    });
    expect(lastHistory().anchor).toMatchObject({
      kind: 'ref',
      hash: head,
      ref: 'refs/heads/feature/山',
    });
    await fixtureGit(root, 'update-ref', 'refs/heads/feature/山', parentHead);
    await request({ version: 1, type: 'loadMore' });
    expect(lastHistory().anchor.hash).toBe(head);
    expect(lastHistory().append).toBe(true);
    await request({ version: 1, type: 'refresh' });
    expect(lastHistory().anchor.hash).toBe(parentHead);
    await request({ version: 1, type: 'focusCommit', hash: parent });
    expect(lastHistory().returnAnchor).toMatchObject({
      kind: 'ref',
      ref: 'refs/heads/feature/山',
    });
    await request({ version: 1, type: 'returnToHistory' });
    expect(lastHistory().anchor).toMatchObject({
      kind: 'ref',
      hash: parentHead,
    });
    await request({ version: 1, type: 'selectRef', name: 'refs/tags/v0.2.0' });
    expect(lastHistory().page.commits[0]?.hash).toBe(parentHead);
    const tagAnchor = lastHistory().anchor;
    await fixtureGit(root, 'tag', '-d', 'v0.2.0');
    await request({ version: 1, type: 'refresh' });
    expect(mock.messages.at(-2)).toMatchObject({ type: 'error' });
    expect(lastHistory().anchor).toEqual(tagAnchor);
    await request({ version: 1, type: 'loadMore' });
    expect(lastHistory().anchor).toEqual(tagAnchor);
    const messageCount = mock.messages.length;
    mock.receive?.({
      version: 1,
      type: 'selectRef',
      name: 'refs/heads/unknown',
    });
    expect(mock.messages).toHaveLength(messageCount);
    await request({ version: 1, type: 'returnToHead' });
    expect(lastHistory().anchor).toMatchObject({ kind: 'head', hash: head });
    await request({ version: 1, type: 'focusCommit', hash: parent });
    expect(lastHistory().page.commits[0]?.hash).toBe(parent);
    const replayStart = mock.messages.length;
    await request({ version: 1, type: 'ready' });
    expect(
      mock.messages
        .slice(replayStart)
        .some(
          (message) =>
            message.type === 'investigation' &&
            message.investigation.commit?.hash === boundary.hash,
        ),
    ).toBe(true);
    expect(
      mock.messages
        .slice(replayStart)
        .some(
          (message) =>
            message.type === 'commit' && message.detail.commit.hash === parent,
        ),
    ).toBe(true);
    expect(await fixtureGit(root, 'rev-parse', 'HEAD')).toBe(head);
    expect(await readFile(join(root, '.git/index'))).toEqual(index);
  } finally {
    panel?.dispose();
    await rm(root, { recursive: true, force: true });
  }
});

it('exports only on an explicit save and restores frozen assessments while denying unknown evidence identities', async () => {
  const root = await createRepository();
  mock.messages = [];
  mock.writes = [];
  mock.clipboard = [];
  let panel: BugsnitchPanel | undefined;
  try {
    const good = await commitFile(root, 'code.ts', 'good\n');
    const bad = await commitFile(root, 'code.ts', 'bad\n');
    panel = new BugsnitchPanel(
      {
        extensionUri: { path: 'file:///extension' },
      } as unknown as ExtensionContext,
      root,
      () => {},
    );
    await request({ version: 1, type: 'ready' });
    await request({ version: 1, type: 'markGood', hash: good });
    await request({ version: 1, type: 'markBad', hash: bad });
    await request({ version: 1, type: 'viewRange' });
    await request({
      version: 1,
      type: 'annotate',
      hash: bad,
      note: 'Reproduction <script> text',
      verdict: 'confirmed',
    });
    const session = [...mock.messages]
      .reverse()
      .find((message) => message.type === 'session');
    expect(session).toMatchObject({
      session: { annotations: [{ hash: bad, verdict: 'confirmed' }] },
    });
    await request({ version: 1, type: 'markBad', hash: bad });
    expect(
      [...mock.messages]
        .reverse()
        .find((message) => message.type === 'session'),
    ).toEqual(session);
    const count = mock.messages.length;
    for (const message of [
      { version: 1, type: 'markBad', hash: 'f'.repeat(40) },
      {
        version: 1,
        type: 'annotate',
        hash: good,
        note: 'Not a candidate',
        verdict: 'confirmed',
      },
      { version: 1, type: 'openDiff', hash: bad, path: 'not-issued.ts' },
      { version: 1, type: 'compareParent', hash: bad, parent: 'f'.repeat(40) },
    ])
      mock.receive?.(message);
    expect(mock.messages).toHaveLength(count);
    expect(mock.writes).toEqual([]);
    mock.destination = undefined;
    await request({ version: 1, type: 'exportInvestigation' });
    expect(mock.writes).toEqual([]);
    mock.destination = { path: join(root, 'explicit-export.json') };
    await request({ version: 1, type: 'exportInvestigation' });
    expect(mock.writes).toHaveLength(1);
    const exported = JSON.parse(mock.writes[0]!.contents);
    expect(exported.annotations[0].note).toContain('<script>');
    expect(mock.writes[0]!.contents).not.toContain(root);
    expect(exported).not.toHaveProperty('diff');
    await request({ version: 1, type: 'ready' });
    expect(
      [...mock.messages]
        .reverse()
        .find((message) => message.type === 'session'),
    ).toEqual(session);
    await request({ version: 1, type: 'copyHash', hash: bad });
    expect(mock.clipboard).toEqual([bad]);
  } finally {
    panel?.dispose();
    mock.destination = undefined;
    await rm(root, { recursive: true, force: true });
  }
});
