// SPDX-License-Identifier: MPL-2.0
import type { Commit } from '@bugsnitch/shared';
import type { ReactNode } from 'react';

export function formatDate(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? 'Unknown date' : date.toLocaleString();
}

export function CommitRow({
  commit,
  selected,
  disabled,
  onInspect,
  graph,
}: {
  commit: Commit;
  selected: boolean;
  disabled: boolean;
  onInspect: (hash: string) => void;
  graph?: ReactNode;
}) {
  return (
    <li className="commit-row">
      <button
        id={`commit-${commit.hash}`}
        className="commit-button"
        aria-pressed={selected}
        disabled={disabled}
        onClick={() => onInspect(commit.hash)}
      >
        {graph}
        <span className="commit-content">
          <span className="commit-top">
            <code>{commit.shortHash}</code>
            <strong>{commit.subject || '(no subject)'}</strong>
          </span>
          <span className="commit-meta">
            {commit.author}
            <span aria-hidden="true"> · </span>
            <time dateTime={commit.authorDate}>
              {formatDate(commit.authorDate)}
            </time>
            {commit.parents.length > 1 ? ' · Merge commit' : ''}
          </span>
          <span className="sr-only">
            {commit.parents.length
              ? `Parents: ${commit.parents.map((parent) => parent.slice(0, 8)).join(', ')}.`
              : 'Root commit with no parents.'}
          </span>
          {commit.refs.length > 0 && (
            <span className="refs" aria-label="References">
              {commit.refs.map((ref) => (
                <span className="ref" key={ref}>
                  {ref}
                </span>
              ))}
            </span>
          )}
        </span>
      </button>
    </li>
  );
}
