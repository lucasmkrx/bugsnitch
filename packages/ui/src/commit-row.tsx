// SPDX-License-Identifier: MPL-2.0
import type { Commit } from '@bugsnitch/shared';

export function formatDate(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? 'Unknown date' : date.toLocaleString();
}

export function CommitRow({
  commit,
  selected,
  disabled,
  onInspect,
}: {
  commit: Commit;
  selected: boolean;
  disabled: boolean;
  onInspect: (hash: string) => void;
}) {
  return (
    <li className="commit-row">
      <button
        className="commit-button"
        aria-pressed={selected}
        disabled={disabled}
        onClick={() => onInspect(commit.hash)}
      >
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
        {commit.refs.length > 0 && (
          <span className="refs" aria-label="References">
            {commit.refs.map((ref) => (
              <span className="ref" key={ref}>
                {ref}
              </span>
            ))}
          </span>
        )}
      </button>
    </li>
  );
}
