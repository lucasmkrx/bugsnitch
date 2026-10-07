// SPDX-License-Identifier: MPL-2.0
export interface Commit {
  hash: string;
  shortHash: string;
  parents: string[];
  author: string;
  authorDate: string;
  subject: string;
  message: string;
  refs: string[];
}

export interface WorkingTreeStatus {
  staged: number;
  modified: number;
  untracked: number;
  conflicted: number;
}

export interface RepositorySnapshot {
  root: string;
  name: string;
  branch: string;
  head: string | null;
  filtersDisabled: boolean;
  status: WorkingTreeStatus;
}

export interface HistoryPage {
  commits: Commit[];
  hasMore: boolean;
}

export type HistoryAnchor =
  | { kind: 'head'; hash: string | null; label: string }
  | { kind: 'commit'; hash: string; label: string }
  | { kind: 'ref'; hash: string; label: string; ref: string };

export interface RepositoryReference {
  name: string;
  label: string;
  kind: 'branch' | 'remote' | 'tag';
  hash: string;
}
export interface ReferencePage {
  references: RepositoryReference[];
  truncated: boolean;
}

export interface LineProvenance {
  hash: string;
  author: string;
  authorDate: string;
  subject: string;
  originalLine: number;
  currentLine: number;
  path: string;
  originalPath: string;
  uncommitted: boolean;
}

export interface Investigation {
  repository: string;
  provenance: LineProvenance;
  commit: Commit | null;
}

export interface ChangedFile {
  status: string;
  path: string;
}

export interface CommitDetail {
  commit: Commit;
  files: ChangedFile[];
  diff: string;
  diffTruncated: boolean;
}
