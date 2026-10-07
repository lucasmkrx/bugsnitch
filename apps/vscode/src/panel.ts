// SPDX-License-Identifier: MPL-2.0
import * as vscode from 'vscode';
import {
  inspectCommit,
  recentHistory,
  repositorySnapshot,
} from '@bugsnitch/git';
import { parseWebviewMessage } from '@bugsnitch/shared';
import type {
  HostMessage,
  Investigation,
  RepositorySnapshot,
} from '@bugsnitch/shared';
import { webviewHtml } from './webview-html';

function errorMessage(error: unknown): string {
  return error instanceof Error
    ? error.message
    : 'Bugsnitch could not complete this investigation.';
}

export class BugsnitchPanel implements vscode.Disposable {
  private readonly panel: vscode.WebviewPanel;
  private readonly subscriptions: vscode.Disposable[] = [];
  private readonly controller = new AbortController();
  private snapshot: RepositorySnapshot | undefined;
  private offset = 0;
  private hasMore = false;
  private busy = false;
  private ready = false;
  private disposed = false;
  private pendingInvestigation: Investigation | undefined;
  private readonly knownCommits = new Set<string>();

  constructor(
    context: vscode.ExtensionContext,
    private readonly root: string,
    onDispose: () => void,
  ) {
    const resources = vscode.Uri.joinPath(context.extensionUri, 'dist');
    this.panel = vscode.window.createWebviewPanel(
      'bugsnitch',
      'Bugsnitch',
      vscode.ViewColumn.Beside,
      {
        enableScripts: true,
        localResourceRoots: [resources],
      },
    );
    this.panel.iconPath = vscode.Uri.joinPath(
      resources,
      'brand',
      'bugsnitch-icon.png',
    );
    const webview = this.panel.webview;
    webview.html = webviewHtml(
      webview.cspSource,
      webview
        .asWebviewUri(vscode.Uri.joinPath(resources, 'webview', 'webview.js'))
        .toString(),
      webview
        .asWebviewUri(vscode.Uri.joinPath(resources, 'webview', 'webview.css'))
        .toString(),
      webview
        .asWebviewUri(
          vscode.Uri.joinPath(resources, 'brand', 'bugsnitch-logo.svg'),
        )
        .toString(),
    );
    this.subscriptions.push(
      webview.onDidReceiveMessage((value: unknown) => {
        const message = parseWebviewMessage(value);
        if (!message || this.busy || this.disposed) return;
        if (message.type === 'ready') {
          this.ready = true;
          void this.task(async () => {
            await this.history(false);
            if (this.pendingInvestigation)
              await this.post({
                version: 1,
                type: 'investigation',
                investigation: this.pendingInvestigation,
              });
          });
        } else if (message.type === 'refresh')
          void this.task(() => this.history(false));
        else if (message.type === 'loadMore' && this.hasMore)
          void this.task(() => this.history(true));
        else if (
          message.type === 'inspectCommit' &&
          this.knownCommits.has(message.hash)
        )
          void this.task(async () => {
            const detail = await inspectCommit(this.root, message.hash, {
              signal: this.controller.signal,
            });
            await this.post({ version: 1, type: 'commit', detail });
          });
      }),
    );
    this.subscriptions.push(
      this.panel.onDidDispose(() => {
        this.disposed = true;
        this.controller.abort();
        for (const subscription of this.subscriptions) subscription.dispose();
        onDispose();
      }),
    );
  }

  reveal(): void {
    this.panel.reveal(vscode.ViewColumn.Beside);
  }
  dispose(): void {
    this.panel.dispose();
  }

  async showInvestigation(investigation: Investigation): Promise<void> {
    this.pendingInvestigation = investigation;
    if (investigation.commit) this.knownCommits.add(investigation.commit.hash);
    this.reveal();
    if (this.ready)
      await this.post({ version: 1, type: 'investigation', investigation });
  }

  private async post(message: HostMessage): Promise<void> {
    if (!this.disposed) await this.panel.webview.postMessage(message);
  }

  private async task(action: () => Promise<void>): Promise<void> {
    this.busy = true;
    await this.post({ version: 1, type: 'busy', busy: true });
    try {
      await action();
    } catch (error: unknown) {
      if (!this.disposed)
        await this.post({
          version: 1,
          type: 'error',
          message: errorMessage(error),
        });
    } finally {
      this.busy = false;
      await this.post({ version: 1, type: 'busy', busy: false });
    }
  }

  private async history(append: boolean): Promise<void> {
    const options = { signal: this.controller.signal };
    const snapshot = append
      ? this.snapshot
      : await repositorySnapshot(this.root, options);
    const offset = append ? this.offset : 0;
    if (!snapshot) return;
    const configured = vscode.workspace
      .getConfiguration('bugsnitch')
      .get<number>('historyPageSize', 50);
    const limit = Number.isFinite(configured)
      ? Math.min(200, Math.max(10, Math.floor(configured)))
      : 50;
    const page = snapshot.head
      ? await recentHistory(this.root, {
          ...options,
          limit,
          skip: offset,
          head: snapshot.head,
        })
      : { commits: [], hasMore: false };
    // Publish state only after the read succeeds, keeping a failed refresh from
    // mixing the previous list with a new snapshot during pagination.
    this.snapshot = snapshot;
    this.offset = offset + page.commits.length;
    if (!append) {
      this.knownCommits.clear();
      if (this.pendingInvestigation?.commit)
        this.knownCommits.add(this.pendingInvestigation.commit.hash);
    }
    this.hasMore = page.hasMore;
    for (const commit of page.commits) this.knownCommits.add(commit.hash);
    await this.post({
      version: 1,
      type: 'history',
      repository: snapshot,
      page,
      append,
    });
  }
}
