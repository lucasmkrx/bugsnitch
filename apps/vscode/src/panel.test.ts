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
  workspace: { getConfiguration: () => ({ get: () => 10 }) },
  window: {
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
    const index = await readFile(join(root, '.git/index'));
    panel = new BugsnitchPanel(
      {
        extensionUri: { path: 'file:///extension' },
      } as unknown as ExtensionContext,
      root,
      () => {},
    );
    await request({ version: 1, type: 'ready' });
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
    await request({ version: 1, type: 'returnToHead' });
    expect(lastHistory().anchor).toMatchObject({ kind: 'head', hash: head });
    await request({ version: 1, type: 'focusCommit', hash: parent });
    expect(lastHistory().page.commits[0]?.hash).toBe(parent);
    expect(await fixtureGit(root, 'rev-parse', 'HEAD')).toBe(head);
    expect(await readFile(join(root, '.git/index'))).toEqual(index);
  } finally {
    panel?.dispose();
    await rm(root, { recursive: true, force: true });
  }
});
