// SPDX-License-Identifier: MPL-2.0
import type { CommitDetail } from '@bugsnitch/shared';
import { formatDate } from './commit-row';

export function CommitInspection({
  detail,
  disabled = false,
  onInspect,
  onShowInGraph,
}: {
  detail: CommitDetail;
  disabled?: boolean;
  onInspect?: (hash: string) => void;
  onShowInGraph?: (hash: string) => void;
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
          Merge changes are compared with the first parent.
        </p>
      )}
      <h3>
        Changed files <span className="count">{detail.files.length}</span>
      </h3>
      {detail.files.length ? (
        <ul className="files">
          {detail.files.map((file) => (
            <li key={file.path}>
              <code>{file.status}</code>
              <span>{file.path}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p>No file changes against the first parent.</p>
      )}
      <h3>Patch</h3>
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
