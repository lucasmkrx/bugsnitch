// SPDX-License-Identifier: MPL-2.0
import { afterEach, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({
  editor: undefined as
    { document: { uri: { scheme: string; fsPath: string } } } | undefined,
  folders: [] as { uri: { scheme: string; fsPath: string } }[],
  pick: vi.fn(),
  find: vi.fn(),
}));
vi.mock('vscode', () => ({
  workspace: {
    get workspaceFolders() {
      return api.folders;
    },
  },
  window: {
    get activeTextEditor() {
      return api.editor;
    },
    showQuickPick: api.pick,
  },
}));
vi.mock('@bugsnitch/git', async (original) => ({
  ...(await original<typeof import('@bugsnitch/git')>()),
  findRepository: api.find,
}));
import { selectRepository } from './repositories';
afterEach(() => {
  api.editor = undefined;
  api.folders = [];
  vi.clearAllMocks();
});
it('prefers the active file repository', async () => {
  api.editor = {
    document: { uri: { scheme: 'file', fsPath: '/workspace/nested/file.ts' } },
  };
  api.find.mockResolvedValue('/nested');
  expect(await selectRepository()).toBe('/nested');
  expect(api.pick).not.toHaveBeenCalled();
});
it('deduplicates roots and tolerates an unavailable sibling workspace', async () => {
  api.folders = ['a', 'b', 'gone'].map((fsPath) => ({
    uri: { scheme: 'file', fsPath },
  }));
  api.find.mockImplementation(async (path: string) => {
    if (path === 'gone') throw new Error('Unavailable root');
    return '/repo';
  });
  expect(await selectRepository()).toBe('/repo');
  expect(api.pick).not.toHaveBeenCalled();
});
it('uses the multiple repository picker and keeps cancellation quiet', async () => {
  api.folders = ['a', 'b'].map((fsPath) => ({
    uri: { scheme: 'file', fsPath },
  }));
  api.find.mockImplementation(async (path: string) => path);
  api.pick.mockResolvedValueOnce({ root: 'b' });
  expect(await selectRepository()).toBe('b');
  api.pick.mockResolvedValueOnce(undefined);
  await expect(selectRepository()).rejects.toMatchObject({ code: 'cancelled' });
});
it('rejects virtual or missing workspaces and retains actionable discovery failures', async () => {
  api.folders = [{ uri: { scheme: 'virtual', fsPath: 'ignored' } }];
  await expect(selectRepository()).rejects.toMatchObject({
    code: 'notRepository',
  });
  api.folders = [{ uri: { scheme: 'file', fsPath: 'failed' } }];
  api.find.mockRejectedValue(new Error('Install Git'));
  await expect(selectRepository()).rejects.toThrow('Install Git');
});
