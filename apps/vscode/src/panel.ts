// SPDX-License-Identifier: MPL-2.0
import * as vscode from 'vscode';
import {
  inspectCommit,
  recentHistory,
  repositorySnapshot,
  listReferences,
  GitError,
} from '@bugsnitch/git';
import { parseWebviewMessage } from '@bugsnitch/shared';
import type {
  HostMessage,
  Investigation,
  CommitDetail,
  HistoryAnchor,
  ReferencePage,
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
  private shownInvestigation: Investigation | undefined;
  private lastDetail: CommitDetail | undefined;
  private anchor: HistoryAnchor | undefined;
  private returnAnchor: HistoryAnchor | null = null;
  private references: ReferencePage = { references: [], truncated: false };
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
          // VS Code can recreate a hidden webview; replay its context.
          this.shownInvestigation = undefined;
          void this.task(async () => {
            await this.history(false);
            await this.publishInvestigation();
            if (this.lastDetail)
              await this.post({
                version: 1,
                type: 'commit',
                detail: this.lastDetail,
              });
          });
        } else if (message.type === 'refresh')
          void this.task(() => this.history(false));
        else if (message.type === 'loadMore' && this.hasMore)
          void this.task(() => this.history(true));
        else if (message.type === 'returnToHead')
          void this.task(() =>
            this.history(false, { kind: 'head', hash: null, label: '' }),
          );
        else if (
          message.type === 'focusCommit' &&
          this.knownCommits.has(message.hash)
        )
          void this.task(() =>
            this.history(false, {
              kind: 'commit',
              hash: message.hash,
              label: `Commit ${message.hash.slice(0, 8)}`,
            }),
          );
        else if (message.type === 'returnToHistory' && this.returnAnchor)
          void this.task(() => this.history(false, this.returnAnchor!));
        else if (message.type === 'selectRef') {
          const ref = this.references.references.find(
            (ref) => ref.name === message.name,
          );
          if (ref)
            void this.task(() =>
              this.history(false, {
                kind: 'ref',
                hash: ref.hash,
                label: ref.label,
                ref: ref.name,
              }),
            );
        } else if (
          message.type === 'inspectCommit' &&
          this.knownCommits.has(message.hash)
        )
          void this.task(async () => {
            const investigation = this.pendingInvestigation;
            const detail = await inspectCommit(this.root, message.hash, {
              signal: this.controller.signal,
            });
            if (investigation !== this.pendingInvestigation) return;
            this.lastDetail = detail;
            for (const parent of detail.commit.parents)
              this.knownCommits.add(parent);
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
    if (this.lastDetail?.commit.hash !== investigation.commit?.hash)
      this.lastDetail = undefined;
    if (investigation.commit) this.knownCommits.add(investigation.commit.hash);
    this.reveal();
    if (this.ready && !this.busy) await this.publishInvestigation();
  }

  private async publishInvestigation(): Promise<void> {
    const investigation = this.pendingInvestigation;
    if (
      !this.ready ||
      !investigation ||
      investigation === this.shownInvestigation
    )
      return;
    this.shownInvestigation = investigation;
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
      await this.publishInvestigation();
      await this.post({ version: 1, type: 'busy', busy: false });
    }
  }

  private async history(
    append: boolean,
    requested?: HistoryAnchor,
  ): Promise<void> {
    const options = { signal: this.controller.signal };
    const snapshot = append
      ? this.snapshot
      : await repositorySnapshot(this.root, options);
    const references = append
      ? this.references
      : await listReferences(this.root, options);
    const offset = append ? this.offset : 0;
    if (!snapshot) return;
    const current = requested ?? this.anchor;
    let anchor: HistoryAnchor =
      current?.kind === 'commit'
        ? current
        : { kind: 'head', hash: snapshot.head, label: snapshot.branch };
    if (current?.kind === 'ref') {
      const reference = references.references.find(
        (ref) => ref.name === current.ref,
      );
      if (!reference)
        throw new GitError(
          'failed',
          'This reference is no longer available in the local reference list. Choose another entry point.',
        );
      anchor = append
        ? current
        : {
            kind: 'ref',
            hash: reference.hash,
            label: reference.label,
            ref: reference.name,
          };
    }
    const returnAnchor =
      requested?.kind === 'commit' && this.anchor?.kind !== 'commit'
        ? (this.anchor ?? null)
        : requested && requested.kind !== 'commit'
          ? null
          : this.returnAnchor;
    const configured = vscode.workspace
      .getConfiguration('bugsnitch')
      .get<number>('historyPageSize', 50);
    const limit = Number.isFinite(configured)
      ? Math.min(200, Math.max(10, Math.floor(configured)))
      : 50;
    const page = anchor.hash
      ? await recentHistory(this.root, {
          ...options,
          limit,
          skip: offset,
          head: anchor.hash,
        })
      : { commits: [], hasMore: false };
    // Publish state only after the read succeeds, keeping a failed refresh from
    // mixing the previous list with a new snapshot during pagination.
    this.snapshot = snapshot;
    this.anchor = anchor;
    this.returnAnchor = returnAnchor;
    this.references = references;
    this.offset = offset + page.commits.length;
    if (!append) {
      this.knownCommits.clear();
      if (this.pendingInvestigation?.commit)
        this.knownCommits.add(this.pendingInvestigation.commit.hash);
      if (this.lastDetail) {
        this.knownCommits.add(this.lastDetail.commit.hash);
        for (const parent of this.lastDetail.commit.parents)
          this.knownCommits.add(parent);
      }
    }
    this.hasMore = page.hasMore;
    for (const commit of page.commits) this.knownCommits.add(commit.hash);
    await this.post({
      version: 1,
      type: 'history',
      repository: snapshot,
      page,
      append,
      anchor,
      returnAnchor,
      references,
    });
  }
}
