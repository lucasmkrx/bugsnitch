// SPDX-License-Identifier: MPL-2.0
import type {
  CommitDetail,
  HistoryPage,
  HistoryAnchor,
  ReferencePage,
  Investigation,
  RepositorySnapshot,
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
        'ready' | 'refresh' | 'loadMore' | 'returnToHead' | 'returnToHistory';
    }
  | { version: 1; type: 'inspectCommit' | 'focusCommit'; hash: string }
  | { version: 1; type: 'selectRef'; name: string };

export type HostMessage =
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
  if (value.type === 'selectRef')
    return keys.length === 3 && isReferenceName(value.name)
      ? { version: 1, type: 'selectRef', name: value.name }
      : null;
  if (value.type === 'inspectCommit' || value.type === 'focusCommit') {
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
      return { version: 1, type: value.type };
    default:
      return null;
  }
}
