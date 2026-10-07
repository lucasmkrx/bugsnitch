// SPDX-License-Identifier: MPL-2.0
import { afterEach, expect, it, vi } from 'vitest';
import type { ExtensionContext } from 'vscode';
const api = vi.hoisted(() => ({
  trusted: true,
  cancelled: false,
  errors: [] as string[],
  warnings: [] as string[],
  handlers: new Map<string, () => Promise<void>>(),
  editor: undefined as
    | {
        document: {
          isUntitled: boolean;
          isDirty: boolean;
          version: number;
          uri: { scheme: string; fsPath: string };
          getText(): string;
        };
        selection: { active: { line: number } };
      }
    | undefined,
  find: vi.fn(),
  investigate: vi.fn(),
  publish: vi.fn(),
  reveal: vi.fn(),
}));
vi.mock('vscode', () => ({
  workspace: {
    get isTrusted() {
      return api.trusted;
    },
  },
  commands: {
    registerCommand: (id: string, handler: () => Promise<void>) => {
      api.handlers.set(id, handler);
      return { dispose() {} };
    },
  },
  window: {
    get activeTextEditor() {
      return api.editor;
    },
    showErrorMessage: async (message: string) => {
      api.errors.push(message);
    },
    showWarningMessage: async (message: string) => {
      api.warnings.push(message);
    },
    withProgress: async (
      _options: unknown,
      work: (progress: unknown, token: unknown) => Promise<void>,
    ) =>
      work(
        {},
        {
          isCancellationRequested: api.cancelled,
          onCancellationRequested: () => ({ dispose() {} }),
        },
      ),
  },
  ProgressLocation: { Notification: 1 },
}));
vi.mock('@bugsnitch/git', async (original) => ({
  ...(await original<typeof import('@bugsnitch/git')>()),
  findRepository: api.find,
  relativeGitPath: () => 'file.ts',
}));
vi.mock('@bugsnitch/forensics', () => ({ investigateLine: api.investigate }));
vi.mock('./panel', () => ({
  BugsnitchPanel: class {
    reveal = api.reveal;
    showInvestigation = api.publish;
    dispose() {}
  },
}));
import { activate } from './extension';
let subscriptions: { dispose(): void }[] = [];
function command() {
  subscriptions = [];
  activate({ subscriptions } as unknown as ExtensionContext);
  return api.handlers.get('bugsnitch.snitchLine')!();
}
function editor() {
  api.editor = {
    document: {
      isUntitled: false,
      isDirty: false,
      version: 1,
      uri: { scheme: 'file', fsPath: '/repo/file.ts' },
      getText: () => 'saved contents',
    },
    selection: { active: { line: 4 } },
  };
}
afterEach(() => {
  for (const subscription of subscriptions) subscription.dispose();
  api.handlers.clear();
  api.errors = [];
  api.warnings = [];
  api.trusted = true;
  api.cancelled = false;
  api.editor = undefined;
  vi.clearAllMocks();
});
it('denies untrusted work before invoking Git', async () => {
  api.trusted = false;
  await command();
  expect(api.warnings).toHaveLength(1);
  expect(api.find).not.toHaveBeenCalled();
});
it('explains absent, untitled, virtual and dirty editors before invoking Git', async () => {
  await command();
  expect(api.errors.at(-1)).toContain('tracked text file');
  for (const change of ['untitled', 'virtual', 'dirty']) {
    editor();
    const document = api.editor!.document;
    if (change === 'untitled') document.isUntitled = true;
    if (change === 'virtual') document.uri.scheme = 'virtual';
    if (change === 'dirty') document.isDirty = true;
    await command();
  }
  expect(api.find).not.toHaveBeenCalled();
  expect(api.errors).toHaveLength(4);
});
it('captures line and content before discovery and discards a changed document result', async () => {
  editor();
  api.find.mockImplementation(async () => {
    api.editor!.selection.active.line = 10;
    api.editor!.document.version++;
    api.editor!.document.isDirty = true;
    return '/repo';
  });
  api.investigate.mockResolvedValue({});
  await command();
  expect(api.investigate).toHaveBeenCalledWith(
    expect.objectContaining({ line: 5, contents: 'saved contents' }),
    expect.anything(),
  );
  expect(api.publish).not.toHaveBeenCalled();
});
it('keeps cancellation quiet during repository discovery', async () => {
  editor();
  api.cancelled = true;
  api.find.mockResolvedValue('/repo');
  await command();
  expect(api.investigate).not.toHaveBeenCalled();
  expect(api.errors).toEqual([]);
});
