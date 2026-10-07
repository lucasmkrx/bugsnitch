// SPDX-License-Identifier: MPL-2.0
import type {
  Commit,
  CommitDetail,
  HistoryPage,
  HistoryAnchor,
  ReferencePage,
  Investigation,
  LineProvenance,
  RepositorySnapshot,
} from './models';
import type { HostMessage } from './protocol';
import {
  isCommitHash,
  isRecord,
  isReferenceName,
  isRepositoryPath,
} from './protocol';

const strings = (value: unknown): value is string[] =>
  Array.isArray(value) &&
  value.every((item: unknown) => typeof item === 'string');
const count = (value: unknown): value is number =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
const date = (value: unknown): value is string =>
  typeof value === 'string' && Number.isFinite(Date.parse(value));
function commit(value: unknown): value is Commit {
  return (
    isRecord(value) &&
    isCommitHash(value.hash) &&
    typeof value.shortHash === 'string' &&
    strings(value.parents) &&
    value.parents.every(isCommitHash) &&
    typeof value.author === 'string' &&
    date(value.authorDate) &&
    typeof value.subject === 'string' &&
    typeof value.message === 'string' &&
    strings(value.refs)
  );
}
function repository(value: unknown): value is RepositorySnapshot {
  return (
    isRecord(value) &&
    typeof value.root === 'string' &&
    typeof value.name === 'string' &&
    typeof value.branch === 'string' &&
    (value.head === null || isCommitHash(value.head)) &&
    typeof value.filtersDisabled === 'boolean' &&
    isRecord(value.status) &&
    count(value.status.staged) &&
    count(value.status.modified) &&
    count(value.status.untracked) &&
    count(value.status.conflicted)
  );
}
function page(value: unknown): value is HistoryPage {
  return (
    isRecord(value) &&
    Array.isArray(value.commits) &&
    value.commits.every(commit) &&
    typeof value.hasMore === 'boolean'
  );
}
function anchor(value: unknown): value is HistoryAnchor {
  return (
    isRecord(value) &&
    typeof value.label === 'string' &&
    ((value.kind === 'head' &&
      (value.hash === null || isCommitHash(value.hash))) ||
      (value.kind === 'commit' && isCommitHash(value.hash)) ||
      (value.kind === 'ref' &&
        isCommitHash(value.hash) &&
        isReferenceName(value.ref)))
  );
}
function references(value: unknown): value is ReferencePage {
  return (
    isRecord(value) &&
    typeof value.truncated === 'boolean' &&
    Array.isArray(value.references) &&
    value.references.every(
      (ref: unknown) =>
        isRecord(ref) &&
        isReferenceName(ref.name) &&
        typeof ref.label === 'string' &&
        (ref.kind === 'branch' ||
          ref.kind === 'remote' ||
          ref.kind === 'tag') &&
        isCommitHash(ref.hash),
    )
  );
}
function provenance(value: unknown): value is LineProvenance {
  return (
    isRecord(value) &&
    isCommitHash(value.hash) &&
    typeof value.author === 'string' &&
    date(value.authorDate) &&
    typeof value.subject === 'string' &&
    count(value.originalLine) &&
    value.originalLine > 0 &&
    count(value.currentLine) &&
    value.currentLine > 0 &&
    typeof value.path === 'string' &&
    typeof value.originalPath === 'string' &&
    typeof value.uncommitted === 'boolean'
  );
}
function investigation(value: unknown): value is Investigation {
  return (
    isRecord(value) &&
    typeof value.repository === 'string' &&
    provenance(value.provenance) &&
    (value.provenance.uncommitted
      ? value.commit === null && /^0+$/.test(value.provenance.hash)
      : commit(value.commit) && value.commit.hash === value.provenance.hash)
  );
}
function detail(value: unknown): value is CommitDetail {
  return (
    isRecord(value) &&
    commit(value.commit) &&
    typeof value.diff === 'string' &&
    typeof value.diffTruncated === 'boolean' &&
    (value.comparisonParent === undefined ||
      value.comparisonParent === null ||
      (isCommitHash(value.comparisonParent) &&
        value.commit.parents.includes(value.comparisonParent))) &&
    (value.selectedPath === undefined ||
      value.selectedPath === null ||
      isRepositoryPath(value.selectedPath)) &&
    Array.isArray(value.files) &&
    value.files.every(
      (file: unknown) =>
        isRecord(file) &&
        isRepositoryPath(file.path) &&
        typeof file.status === 'string',
    )
  );
}

export function isHostMessage(value: unknown): value is HostMessage {
  if (!isRecord(value) || value.version !== 1) return false;
  switch (value.type) {
    case 'fileHistory':
      return (
        isRecord(value.history) &&
        isRepositoryPath(value.history.path) &&
        isCommitHash(value.history.tip) &&
        page(value.history) &&
        value.history.commits.length <= 100
      );
    case 'session': {
      const session = value.session;
      if (!isRecord(session)) return false;
      return (
        (session.good === null || commit(session.good)) &&
        (session.bad === null || commit(session.bad)) &&
        (session.range === null ||
          (isRecord(session.range) &&
            isCommitHash(session.range.good) &&
            isCommitHash(session.range.bad) &&
            page(session.range) &&
            session.range.commits.length <= 200 &&
            isRecord(session.good) &&
            isRecord(session.bad) &&
            session.range.good === session.good.hash &&
            session.range.bad === session.bad.hash)) &&
        Array.isArray(session.annotations) &&
        session.annotations.length <= 200 &&
        session.annotations.every(
          (item: unknown) =>
            isRecord(item) &&
            isCommitHash(item.hash) &&
            isRecord(session.range) &&
            Array.isArray(session.range.commits) &&
            session.range.commits.some(
              (candidate: unknown) =>
                isRecord(candidate) && candidate.hash === item.hash,
            ) &&
            typeof item.note === 'string' &&
            item.note.length <= 4000 &&
            ['candidate', 'confirmed', 'excluded'].includes(
              String(item.verdict),
            ),
        )
      );
    }
    case 'history':
      return (
        repository(value.repository) &&
        page(value.page) &&
        typeof value.append === 'boolean' &&
        anchor(value.anchor) &&
        (value.returnAnchor === null || anchor(value.returnAnchor)) &&
        references(value.references)
      );
    case 'investigation':
      return investigation(value.investigation);
    case 'commit':
      return detail(value.detail);
    case 'busy':
      return typeof value.busy === 'boolean';
    case 'error':
      return typeof value.message === 'string';
    default:
      return false;
  }
}
