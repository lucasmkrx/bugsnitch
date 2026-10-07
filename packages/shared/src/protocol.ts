// SPDX-License-Identifier: MPL-2.0
import type {
  CommitDetail,
  HistoryPage,
  Investigation,
  RepositorySnapshot,
} from './models';

export const PROTOCOL_VERSION = 1;
export const isCommitHash = (value: unknown): value is string =>
  typeof value === 'string' && /^(?:[a-f0-9]{40}|[a-f0-9]{64})$/.test(value);

export type WebviewMessage =
  | { version: 1; type: 'ready' | 'refresh' | 'loadMore' }
  | { version: 1; type: 'inspectCommit'; hash: string };

export type HostMessage =
  | {
      version: 1;
      type: 'history';
      repository: RepositorySnapshot;
      page: HistoryPage;
      append: boolean;
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
  if (value.type === 'inspectCommit') {
    return keys.length === 3 && isCommitHash(value.hash)
      ? { version: 1, type: 'inspectCommit', hash: value.hash }
      : null;
  }
  if (keys.length !== 2) return null;
  switch (value.type) {
    case 'ready':
    case 'refresh':
    case 'loadMore':
      return { version: 1, type: value.type };
    default:
      return null;
  }
}
