// SPDX-License-Identifier: MPL-2.0
import * as vscode from 'vscode';
import { dirname } from 'node:path';
import { findRepository, GitError } from '@bugsnitch/git';

export async function selectRepository(): Promise<string> {
  const editor = vscode.window.activeTextEditor;
  if (editor?.document.uri.scheme === 'file') {
    const root = await findRepository(dirname(editor.document.uri.fsPath));
    if (root) return root;
  }
  const folders = vscode.workspace.workspaceFolders ?? [];
  const roots = [
    ...new Set(
      (
        await Promise.all(
          folders
            .filter((folder) => folder.uri.scheme === 'file')
            .map((folder) => findRepository(folder.uri.fsPath)),
        )
      ).filter((root): root is string => root !== null),
    ),
  ];
  if (roots.length === 1) return roots[0]!;
  if (roots.length > 1) {
    const selected = await vscode.window.showQuickPick(
      roots.map((root) => ({ label: root, root })),
      { title: 'Bugsnitch · Choose a repository' },
    );
    if (selected) return selected.root;
    throw new GitError('cancelled', 'Repository selection was cancelled.');
  }
  throw new GitError(
    'notRepository',
    'Open a Git repository folder or a tracked file, then run Bugsnitch again.',
  );
}
