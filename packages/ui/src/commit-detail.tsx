// SPDX-License-Identifier: MPL-2.0
import type { CommitDetail } from '@bugsnitch/shared';
import { formatDate } from './commit-row';

export function CommitInspection({
  detail,
  disabled = false,
  onInspect,
  onShowInGraph,
  onCopyHash,
  onMarkGood,
  onMarkBad,
  onFileAction,
  onCompareParent,
}: {
  detail: CommitDetail;
  disabled?: boolean;
  onInspect?: (hash: string) => void;
  onShowInGraph?: (hash: string) => void;
  onCopyHash?: (hash: string) => void;
  onMarkGood?: (hash: string) => void;
  onMarkBad?: (hash: string) => void;
  onFileAction?: (
    path: string,
    action: 'patch' | 'diff' | 'history' | 'copy',
  ) => void;
  onCompareParent?: (parent: string) => void;
}) {
  return (
    <section aria-labelledby="commit-title" className="commit-detail">
      <p className="eyebrow">Commit investigation</p>
      <h2 id="commit-title" tabIndex={-1}>
        {detail.commit.subject || '(no subject)'}
      </h2>
      <p>
        {detail.commit.author} · {formatDate(detail.commit.authorDate)}
      </p>
      <code className="full-hash">{detail.commit.hash}</code>
      <div className="action-group">
        {onCopyHash && (
          <button
            className="secondary"
            disabled={disabled}
            onClick={() => onCopyHash(detail.commit.hash)}
          >
            Copy commit hash
          </button>
        )}
        {onMarkGood && (
          <button
            className="secondary"
            disabled={disabled}
            onClick={() => onMarkGood(detail.commit.hash)}
          >
            Mark known good
          </button>
        )}
        {onMarkBad && (
          <button
            className="secondary"
            disabled={disabled}
            onClick={() => onMarkBad(detail.commit.hash)}
          >
            Mark known bad
          </button>
        )}
      </div>
      {onShowInGraph && (
        <p className="inspection-actions">
          <button
            className="secondary"
            disabled={disabled}
            onClick={() => onShowInGraph(detail.commit.hash)}
          >
            Show in graph
          </button>
        </p>
      )}
      <pre className="message">{detail.commit.message}</pre>
      <h3>Parents</h3>
      {detail.commit.parents.length ? (
        <ol className="parents">
          {detail.commit.parents.map((hash, index) => (
            <li key={hash}>
              {onInspect ? (
                <button
                  className="secondary"
                  disabled={disabled}
                  onClick={() => onInspect(hash)}
                  aria-label={`Inspect parent ${index + 1}: ${hash}`}
                >
                  <code>{hash.slice(0, 8)}</code>
                  {index === 0 ? ' · First parent' : ` · Parent ${index + 1}`}
                </button>
              ) : (
                <code>{hash}</code>
              )}
            </li>
          ))}
        </ol>
      ) : (
        <p>No parents recorded in this local history.</p>
      )}
      {detail.commit.parents.length > 1 && (
        <p className="hint">
          Merge changes are compared with{' '}
          {detail.comparisonParent === detail.commit.parents[0] ||
          !detail.comparisonParent
            ? 'the first parent'
            : 'the selected parent'}
          .
        </p>
      )}
      {detail.commit.parents.length > 1 && onCompareParent && (
        <label>
          Compare changes against
          <select
            aria-label="Comparison parent"
            disabled={disabled}
            value={detail.comparisonParent ?? detail.commit.parents[0]}
            onChange={(event) => onCompareParent(event.target.value)}
          >
            {detail.commit.parents.map((hash, index) => (
              <option key={hash} value={hash}>
                Parent {index + 1} · {hash.slice(0, 8)}
              </option>
            ))}
          </select>
        </label>
      )}
      <h3>
        Changed files <span className="count">{detail.files.length}</span>
      </h3>
      {detail.files.length ? (
        <ul className="files">
          {detail.files.map((file) => (
            <li key={file.path}>
              <code>
                {(
                  {
                    A: 'Added',
                    M: 'Modified',
                    D: 'Deleted',
                    T: 'Type changed',
                    U: 'Unmerged',
                  } as Record<string, string>
                )[file.status] ?? file.status}
              </code>
              <div>
                <span>{file.path}</span>
                {onFileAction && (
                  <div className="action-group">
                    <button
                      className="secondary"
                      disabled={disabled}
                      onClick={() => onFileAction(file.path, 'patch')}
                      aria-label={`Preview patch for ${file.path}`}
                    >
                      Patch
                    </button>
                    <button
                      className="secondary"
                      disabled={disabled}
                      onClick={() => onFileAction(file.path, 'diff')}
                      aria-label={`Open diff for ${file.path}`}
                    >
                      Open diff
                    </button>
                    <button
                      className="secondary"
                      disabled={disabled}
                      onClick={() => onFileAction(file.path, 'history')}
                      aria-label={`File history for ${file.path}`}
                    >
                      History
                    </button>
                    <button
                      className="secondary"
                      disabled={disabled}
                      onClick={() => onFileAction(file.path, 'copy')}
                      aria-label={`Copy path ${file.path}`}
                    >
                      Copy path
                    </button>
                  </div>
                )}
              </div>
            </li>
          ))}
        </ul>
      ) : (
        <p>No file changes against the first parent.</p>
      )}
      <h3>Patch{detail.selectedPath ? ` · ${detail.selectedPath}` : ''}</h3>
      {detail.diffTruncated && (
        <p role="status">
          Patch preview is limited to 256 KiB. Use your Git tools for the
          complete diff.
        </p>
      )}
      <pre className="diff" tabIndex={0} aria-label="Commit patch">
        {detail.diff || 'No textual patch to display.'}
      </pre>
    </section>
  );
}
