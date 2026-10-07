// SPDX-License-Identifier: MPL-2.0
import * as vscode from 'vscode';
import { dirname } from 'node:path';
import { findRepository, GitError, relativeGitPath } from '@bugsnitch/git';
import { investigateLine } from '@bugsnitch/forensics';
import { BugsnitchPanel } from './panel';
import { selectRepository } from './repositories';

export function activate(context: vscode.ExtensionContext): void {
  const panels = new Map<string, BugsnitchPanel>();
  const panelFor = (root: string) => {
    let panel = panels.get(root);
    if (!panel) {
      panel = new BugsnitchPanel(context, root, () => panels.delete(root));
      panels.set(root, panel);
    }
    return panel;
  };
  const safely = (action: () => Promise<void>) => async () => {
    if (!vscode.workspace.isTrusted) {
      await vscode.window.showWarningMessage(
        'Trust this workspace before running local Git investigations.',
      );
      return;
    }
    try {
      await action();
    } catch (error: unknown) {
      if (error instanceof GitError && error.code === 'cancelled') return;
      await vscode.window.showErrorMessage(
        `Bugsnitch: ${error instanceof Error ? error.message : 'Could not complete this investigation.'}`,
      );
    }
  };
  context.subscriptions.push(
    vscode.commands.registerCommand(
      'bugsnitch.open',
      safely(async () => {
        panelFor(await selectRepository()).reveal();
      }),
    ),
    vscode.commands.registerCommand(
      'bugsnitch.snitchLine',
      safely(async () => {
        const editor = vscode.window.activeTextEditor;
        if (!editor)
          throw new GitError(
            'invalidInput',
            'Open a tracked text file and select the line to investigate.',
          );
        const document = editor.document;
        if (document.isUntitled || document.uri.scheme !== 'file')
          throw new GitError(
            'invalidInput',
            'Save this file inside a Git repository before investigating it.',
          );
        if (document.isDirty)
          throw new GitError(
            'invalidInput',
            'Save your changes before investigating this line so the result matches the saved file.',
          );
        const root = await findRepository(dirname(document.uri.fsPath));
        if (!root)
          throw new GitError(
            'notRepository',
            'This file is outside a Git repository. Open a tracked file to investigate.',
          );
        const path = relativeGitPath(root, document.uri.fsPath);
        const line = editor.selection.active.line + 1;
        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: 'Bugsnitch · Tracing this line',
            cancellable: true,
          },
          async (_progress, token) => {
            const controller = new AbortController();
            const subscription = token.onCancellationRequested(() =>
              controller.abort(),
            );
            try {
              const investigation = await investigateLine(
                { repository: root, path, line, contents: document.getText() },
                { signal: controller.signal },
              );
              if (!token.isCancellationRequested)
                await panelFor(root).showInvestigation(investigation);
            } finally {
              subscription.dispose();
            }
          },
        );
      }),
    ),
    {
      dispose: () => {
        for (const panel of [...panels.values()]) panel.dispose();
      },
    },
  );
}
