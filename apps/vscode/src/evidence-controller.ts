// SPDX-License-Identifier: MPL-2.0
import * as vscode from 'vscode';
import {
  GitError,
  fileHistory,
  historicalFile,
  inspectCommit,
  readCommit,
  regressionRange,
} from '@bugsnitch/git';
import type {
  CommitDetail,
  FileHistory,
  HostMessage,
  InvestigationSession,
  WebviewMessage,
} from '@bugsnitch/shared';

interface EvidenceHost {
  root: string;
  signal: AbortSignal;
  known(hash: string): boolean;
  addKnown(hash: string): void;
  detail(): CommitDetail | undefined;
  setDetail(detail: CommitDetail): Promise<void>;
  post(message: HostMessage): Promise<boolean>;
}
let sequence = 0;
/** All evidence actions are serialized by the panel and use host-issued identities. */
export class EvidenceController {
  private session: InvestigationSession = {
    good: null,
    bad: null,
    range: null,
    annotations: [],
  };
  private history: FileHistory | undefined;
  private readonly scheme = `bugsnitch-${++sequence}`;
  private documents = new Map<string, string>();
  private documentSequence = 0;
  private provider: vscode.Disposable | undefined;
  constructor(private readonly host: EvidenceHost) {}

  handle(message: WebviewMessage): (() => Promise<void>) | null {
    const host = this.host;
    const options = { signal: host.signal };
    if (message.type === 'clearRange')
      return async () => {
        this.session = { good: null, bad: null, range: null, annotations: [] };
        await this.publishSession();
      };
    if (message.type === 'exportInvestigation')
      return async () => {
        const destination = await vscode.window.showSaveDialog({
          filters: { 'Investigation JSON': ['json'] },
          saveLabel: 'Export investigation',
        });
        if (destination && !host.signal.aborted)
          await vscode.workspace.fs.writeFile(
            destination,
            Buffer.from(
              JSON.stringify({ version: 1, ...this.session }, null, 2),
            ),
          );
      };
    if (message.type === 'viewRange' && this.session.good && this.session.bad)
      return async () => {
        const range = await regressionRange(
          host.root,
          this.session.good!.hash,
          this.session.bad!.hash,
          options,
        );
        this.session = { ...this.session, range };
        for (const commit of range.commits) host.addKnown(commit.hash);
        await this.publishSession();
      };
    if (!('hash' in message) || !host.known(message.hash)) return null;
    if (message.type === 'copyHash')
      return async () => {
        await vscode.env.clipboard.writeText(message.hash);
      };
    if (message.type === 'markGood' || message.type === 'markBad')
      return async () => {
        const endpoint = message.type === 'markGood' ? 'good' : 'bad';
        if (this.session[endpoint]?.hash === message.hash) {
          await this.publishSession();
          return;
        }
        const commit = await readCommit(host.root, message.hash, options);
        this.session = {
          ...this.session,
          [message.type === 'markGood' ? 'good' : 'bad']: commit,
          range: null,
          annotations: [],
        };
        await this.publishSession();
      };
    if (
      message.type === 'annotate' &&
      this.session.range?.commits.some((commit) => commit.hash === message.hash)
    )
      return async () => {
        this.session = {
          ...this.session,
          annotations: [
            ...this.session.annotations.filter(
              (entry) => entry.hash !== message.hash,
            ),
            {
              hash: message.hash,
              note: message.note,
              verdict: message.verdict,
            },
          ],
        };
        await this.publishSession();
      };
    const detail = host.detail();
    if (detail?.commit.hash !== message.hash) return null;
    if (
      message.type === 'compareParent' &&
      detail.commit.parents.includes(message.parent)
    )
      return async () => {
        await host.setDetail(
          await inspectCommit(host.root, message.hash, {
            ...options,
            parent: message.parent,
          }),
        );
      };
    if (
      !('path' in message) ||
      !detail.files.some((file) => file.path === message.path)
    )
      return null;
    if (message.type === 'copyPath')
      return async () => {
        await vscode.env.clipboard.writeText(message.path);
      };
    if (message.type === 'fileHistory')
      return async () => {
        this.history = await fileHistory(
          host.root,
          message.hash,
          message.path,
          options,
        );
        for (const commit of this.history.commits) host.addKnown(commit.hash);
        await host.post({
          version: 1,
          type: 'fileHistory',
          history: this.history,
        });
      };
    if (message.type === 'inspectFile')
      return async () => {
        const next = await inspectCommit(host.root, message.hash, {
          ...options,
          ...(detail.comparisonParent
            ? { parent: detail.comparisonParent }
            : {}),
          path: message.path,
        });
        await host.setDetail({ ...next, files: detail.files });
      };
    if (message.type === 'openDiff')
      return () => this.openDiff(detail, message.path);
    return null;
  }

  private async openDiff(detail: CommitDetail, path: string): Promise<void> {
    const parent = detail.comparisonParent ?? detail.commit.parents[0];
    const options = { signal: this.host.signal };
    const [before, after] = await Promise.all([
      parent
        ? historicalFile(this.host.root, parent, path, options)
        : Promise.resolve(''),
      historicalFile(this.host.root, detail.commit.hash, path, options),
    ]);
    if (this.host.signal.aborted) return;
    this.provider ??= vscode.workspace.registerTextDocumentContentProvider(
      this.scheme,
      {
        provideTextDocumentContent: (uri) => {
          const contents = this.documents.get(uri.toString());
          if (contents === undefined)
            throw new GitError(
              'failed',
              'This historical preview has expired. Open the diff again from Bugsnitch.',
            );
          return contents;
        },
      },
    );
    const makeDocument = (contents: string, hash: string) => {
      const uri = vscode.Uri.from({
        scheme: this.scheme,
        path: `/${path}`,
        query: `${hash}-${++this.documentSequence}`,
      });
      this.documents.set(uri.toString(), contents);
      return uri;
    };
    const left = makeDocument(before, parent ?? 'empty');
    const right = makeDocument(after, detail.commit.hash);
    while (this.documents.size > 8)
      this.documents.delete(this.documents.keys().next().value!);
    await vscode.commands.executeCommand(
      'vscode.diff',
      left,
      right,
      `${path} · ${parent?.slice(0, 8) ?? 'Empty tree'} ↔ ${detail.commit.shortHash}`,
      { viewColumn: vscode.ViewColumn.Beside, preview: false },
    );
  }

  private async publishSession(): Promise<void> {
    await this.host.post({
      version: 1,
      type: 'session',
      session: this.session,
    });
  }
  async replay(): Promise<void> {
    await this.publishSession();
    if (this.history)
      await this.host.post({
        version: 1,
        type: 'fileHistory',
        history: this.history,
      });
  }
  knownHashes(): string[] {
    return [
      ...(this.history?.commits.map((commit) => commit.hash) ?? []),
      ...(this.session.range?.commits.map((commit) => commit.hash) ?? []),
      ...(this.session.good ? [this.session.good.hash] : []),
      ...(this.session.bad ? [this.session.bad.hash] : []),
    ];
  }
  dispose(): void {
    this.documents.clear();
    this.provider?.dispose();
  }
}
