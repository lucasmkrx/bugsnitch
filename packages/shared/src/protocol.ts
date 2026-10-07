// SPDX-License-Identifier: MPL-2.0
import type {
  CommitDetail,
  HistoryPage,
  HistoryAnchor,
  ReferencePage,
  Investigation,
  RepositorySnapshot,
  FileHistory,
  InvestigationSession,
} from './models';

export const PROTOCOL_VERSION = 1;
export const isCommitHash = (value: unknown): value is string =>
  typeof value === 'string' && /^(?:[a-f0-9]{40}|[a-f0-9]{64})$/.test(value);
export const isReferenceName = (value: unknown): value is string =>
  typeof value === 'string' &&
  value.length <= 4096 &&
  /^refs\/(?:heads|remotes|tags)\/.+$/u.test(value) &&
  ![...value].some(
    (char) => char.charCodeAt(0) <= 32 || char.charCodeAt(0) === 127,
  );

export type WebviewMessage =
  | {
      version: 1;
      type:
        | 'ready'
        | 'refresh'
        | 'loadMore'
        | 'returnToHead'
        | 'returnToHistory'
        | 'viewRange'
        | 'clearRange'
        | 'exportInvestigation';
    }
  | {
      version: 1;
      type:
        'inspectCommit' | 'focusCommit' | 'copyHash' | 'markGood' | 'markBad';
      hash: string;
    }
  | {
      version: 1;
      type: 'inspectFile' | 'openDiff' | 'fileHistory' | 'copyPath';
      hash: string;
      path: string;
    }
  | { version: 1; type: 'compareParent'; hash: string; parent: string }
  | {
      version: 1;
      type: 'annotate';
      hash: string;
      note: string;
      verdict: 'candidate' | 'confirmed' | 'excluded';
    }
  | { version: 1; type: 'selectRef'; name: string };

export type HostMessage =
  | { version: 1; type: 'fileHistory'; history: FileHistory }
  | { version: 1; type: 'session'; session: InvestigationSession }
  | {
      version: 1;
      type: 'history';
      repository: RepositorySnapshot;
      page: HistoryPage;
      append: boolean;
      anchor: HistoryAnchor;
      returnAnchor: HistoryAnchor | null;
      references: ReferencePage;
    }
  | { version: 1; type: 'investigation'; investigation: Investigation }
  | { version: 1; type: 'commit'; detail: CommitDetail }
  | { version: 1; type: 'busy'; busy: boolean }
  | { version: 1; type: 'error'; message: string };

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

export function parseWebviewMessage(value: unknown): WebviewMessage | null {
  if (!isRecord(value) || value.version !== PROTOCOL_VERSION) return null;
  const keys = Object.keys(value);
  if (
    ['inspectFile', 'openDiff', 'fileHistory', 'copyPath'].includes(
      String(value.type),
    )
  ) {
    return keys.length === 4 &&
      isCommitHash(value.hash) &&
      isRepositoryPath(value.path)
      ? {
          version: 1,
          type: value.type as
            'inspectFile' | 'openDiff' | 'fileHistory' | 'copyPath',
          hash: value.hash,
          path: value.path,
        }
      : null;
  }
  if (value.type === 'compareParent')
    return keys.length === 4 &&
      isCommitHash(value.hash) &&
      isCommitHash(value.parent)
      ? {
          version: 1,
          type: 'compareParent',
          hash: value.hash,
          parent: value.parent,
        }
      : null;
  if (value.type === 'annotate')
    return keys.length === 5 &&
      isCommitHash(value.hash) &&
      typeof value.note === 'string' &&
      value.note.length <= 4000 &&
      ['candidate', 'confirmed', 'excluded'].includes(String(value.verdict))
      ? {
          version: 1,
          type: 'annotate',
          hash: value.hash,
          note: value.note,
          verdict: value.verdict as 'candidate' | 'confirmed' | 'excluded',
        }
      : null;
  if (value.type === 'selectRef')
    return keys.length === 3 && isReferenceName(value.name)
      ? { version: 1, type: 'selectRef', name: value.name }
      : null;
  if (
    value.type === 'inspectCommit' ||
    value.type === 'focusCommit' ||
    value.type === 'copyHash' ||
    value.type === 'markGood' ||
    value.type === 'markBad'
  ) {
    return keys.length === 3 && isCommitHash(value.hash)
      ? { version: 1, type: value.type, hash: value.hash }
      : null;
  }
  if (keys.length !== 2) return null;
  switch (value.type) {
    case 'ready':
    case 'refresh':
    case 'loadMore':
    case 'returnToHead':
    case 'returnToHistory':
    case 'viewRange':
    case 'clearRange':
    case 'exportInvestigation':
      return { version: 1, type: value.type };
    default:
      return null;
  }
}

export const isRepositoryPath = (value: unknown): value is string =>
  typeof value === 'string' &&
  value.length > 0 &&
  value.length <= 4096 &&
  !value.includes('\0') &&
  !/^(?:[/\\]|[a-z]:[/\\])/i.test(value) &&
  !value.replaceAll('\\', '/').split('/').includes('..');
