// SPDX-License-Identifier: MPL-2.0
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import type { HostMessage, WebviewMessage } from '@bugsnitch/shared';
import vscode from 'vscode';

// Observe real VS Code API objects; do not replace the extension host with mocks.
const execute = promisify(execFile);
type History = Extract<HostMessage, { type: 'history' }>;
type Mutable<T> = { -readonly [K in keyof T]: T[K] };
interface Record {
  panel: import('vscode').WebviewPanel;
  messages: HostMessage[];
  handlers: ((message: unknown) => unknown)[];
}
async function waitFor(predicate: () => boolean, label: string) {
  const deadline = Date.now() + 30_000;
  while (!predicate()) {
    assert.ok(Date.now() < deadline, `Timed out: ${label}`);
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
}
function history(messages: HostMessage[]): History {
  const message = [...messages]
    .reverse()
    .find((message) => message.type === 'history');
  assert.ok(message?.type === 'history', 'Expected history response');
  return message;
}
async function request(record: Record, message: WebviewMessage) {
  const start = record.messages.length;
  for (const handler of record.handlers) handler(message);
  await waitFor(() => {
    const last = record.messages.at(-1);
    return (
      record.messages.length > start && last?.type === 'busy' && !last.busy
    );
  }, message.type);
  const responses = record.messages.slice(start);
  assert.ok(
    !responses.some((response) => response.type === 'error'),
    JSON.stringify(responses),
  );
  return responses;
}
export async function run(): Promise<void> {
  const fixture = JSON.parse(process.env.BUGSNITCH_TEST_FIXTURE ?? '{}') as {
    root: string;
    good: string;
    suspect: string;
    head: string;
  };
  assert.ok(fixture.root && fixture.good && fixture.suspect && fixture.head);
  const manifest = JSON.parse(
    await readFile(join(__dirname, '../../package.json'), 'utf8'),
  ) as {
    publisher: string;
    name: string;
  };
  const extension = vscode.extensions.getExtension(
    `${manifest.publisher}.${manifest.name}`,
  );
  assert.ok(extension, 'Bugsnitch extension is loaded');
  const records: Record[] = [];
  const errors: string[] = [];
  const createPanel = vscode.window.createWebviewPanel;
  const showError = vscode.window.showErrorMessage;
  vscode.window.showErrorMessage = (async (message: string) => {
    errors.push(message);
    return undefined;
  }) as typeof showError;
  vscode.window.createWebviewPanel = (...args) => {
    const panel = createPanel(...args);
    const record: Record = { panel, messages: [], handlers: [] };
    records.push(record);
    const post = panel.webview.postMessage.bind(panel.webview);
    panel.webview.postMessage = (message: HostMessage) => {
      record.messages.push(message);
      return post(message);
    };
    const observed = panel.webview as Mutable<import('vscode').Webview>;
    const receive = observed.onDidReceiveMessage.bind(observed);
    observed.onDidReceiveMessage = (handler, ...rest) => {
      record.handlers.push(handler);
      return receive(handler, ...rest);
    };
    return panel;
  };
  try {
    const index = await readFile(join(fixture.root, '.git/index'));
    const source = await readFile(join(fixture.root, 'checkout.js'));
    await extension.activate();
    const document = await vscode.workspace.openTextDocument(
      vscode.Uri.file(join(fixture.root, 'checkout.js')),
    );
    const editor = await vscode.window.showTextDocument(document);
    editor.selection = new vscode.Selection(1, 0, 1, 0);
    await vscode.commands.executeCommand('bugsnitch.open');
    const record = records.at(-1);
    assert.ok(record, 'Open creates a real webview');
    // Await the real bundled webview's ready message, not a synthetic ready event.
    await waitFor(
      () =>
        record.messages.some((message) => message.type === 'history') ||
        errors.length > 0,
      'webview ready',
    );
    await waitFor(() => {
      const last = record.messages.at(-1);
      return last?.type === 'busy' && !last.busy;
    }, 'initial idle');
    assert.deepEqual(errors, []);
    assert.equal(history(record.messages).repository.head, fixture.head);
    assert.equal(history(record.messages).page.commits.length, 10);
    const initial = history(record.messages);
    await request(record, { version: 1, type: 'loadMore' });
    assert.equal(history(record.messages).append, true);
    assert.ok(
      !history(record.messages).page.commits.some((commit) =>
        initial.page.commits.some((previous) => previous.hash === commit.hash),
      ),
    );
    await vscode.window.showTextDocument(document, vscode.ViewColumn.One);
    await vscode.commands.executeCommand('bugsnitch.snitchLine');
    await waitFor(
      () =>
        record.messages.some((message) => message.type === 'investigation') ||
        errors.length > 0,
      'line evidence',
    );
    assert.deepEqual(errors, []);
    const investigation = record.messages.find(
      (message) => message.type === 'investigation',
    );
    assert.ok(investigation?.type === 'investigation');
    assert.equal(investigation.investigation.provenance.hash, fixture.suspect);
    assert.equal(investigation.investigation.provenance.currentLine, 2);
    await request(record, {
      version: 1,
      type: 'inspectCommit',
      hash: fixture.suspect,
    });
    const detail = record.messages.find((message) => message.type === 'commit');
    assert.ok(detail?.type === 'commit');
    assert.equal(detail.detail.commit.parents[0], fixture.good);
    assert.ok(
      detail.detail.diff.includes(
        '-  return items.reduce((sum, item) => sum + item.price * item.quantity, 0);',
      ),
    );
    await request(record, {
      version: 1,
      type: 'inspectCommit',
      hash: fixture.good,
    });
    for (const [name, hash] of [
      ['refs/heads/feature/faster-totals', fixture.suspect],
      ['refs/tags/known-good', fixture.good],
    ]) {
      assert.ok(name && hash);
      await request(record, { version: 1, type: 'selectRef', name });
      assert.equal(history(record.messages).anchor.hash, hash);
      assert.equal(history(record.messages).repository.head, fixture.head);
    }
    await request(record, {
      version: 1,
      type: 'focusCommit',
      hash: fixture.suspect,
    });
    assert.equal(history(record.messages).anchor.kind, 'commit');
    await request(record, { version: 1, type: 'returnToHistory' });
    assert.equal(history(record.messages).anchor.hash, fixture.good);
    await request(record, { version: 1, type: 'returnToHead' });
    assert.equal(history(record.messages).anchor.hash, fixture.head);
    const count = record.messages.length;
    for (const handler of record.handlers)
      handler({ version: 1, type: 'selectRef', name: 'refs/heads/unknown' });
    assert.equal(record.messages.length, count, 'Unknown ref is ignored');
    assert.deepEqual(
      await readFile(join(fixture.root, '.git/index')),
      index,
      'Index unchanged',
    );
    assert.deepEqual(
      await readFile(join(fixture.root, 'checkout.js')),
      source,
      'Working file unchanged',
    );
    const result = await execute('git', [
      '-C',
      fixture.root,
      'rev-parse',
      'HEAD',
    ]);
    assert.equal(result.stdout.trim(), fixture.head, 'Checkout unchanged');
    assert.deepEqual(errors, []);
    console.log(
      'PASS: VS Code host — Open, line attribution, pagination, inspection, parent/ref navigation, graph focus, unchanged checkout/index/file',
    );
  } finally {
    vscode.window.createWebviewPanel = createPanel;
    vscode.window.showErrorMessage = showError;
    for (const record of records) record.panel.dispose();
  }
}
