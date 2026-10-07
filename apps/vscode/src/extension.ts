// SPDX-License-Identifier: MPL-2.0
import * as vscode from 'vscode';
import { dirname } from 'node:path';
import { findRepository, GitError, relativeGitPath } from '@bugsnitch/git';
import { investigateLine } from '@bugsnitch/forensics';
import { BugsnitchPanel } from './panel';
import { selectRepository } from './repositories';
import { InvestigationRequests } from './investigation-requests';

export function activate(context: vscode.ExtensionContext): void {
  const panels = new Map<string, BugsnitchPanel>();
  const requests = new InvestigationRequests();
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
        const file = document.uri.fsPath;
        const version = document.version;
        const contents = document.getText();
        const line = editor.selection.active.line + 1;
        const request = requests.begin(file);
        await vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: 'Bugsnitch · Tracing this line',
            cancellable: true,
          },
          async (_progress, token) => {
            const subscription = token.onCancellationRequested(() =>
              request.cancel(),
            );
            try {
              if (token.isCancellationRequested) request.cancel();
              const root = await findRepository(dirname(file), {
                signal: request.signal,
              });
              if (!root)
                throw new GitError(
                  'notRepository',
                  'This file is outside a Git repository. Open a tracked file to investigate.',
                );
              if (!request.claim(root)) return;
              const path = relativeGitPath(root, file);
              const investigation = await investigateLine(
                { repository: root, path, line, contents },
                { signal: request.signal },
              );
              if (
                request.current(root) &&
                document.version === version &&
                !document.isDirty
              )
                await panelFor(root).showInvestigation(investigation);
            } finally {
              subscription.dispose();
              request.finish();
            }
          },
        );
      }),
    ),
    {
      dispose: () => {
        requests.dispose();
        for (const panel of [...panels.values()]) panel.dispose();
      },
    },
  );
}
