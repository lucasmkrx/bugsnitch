// SPDX-License-Identifier: MPL-2.0
import type { CommitDetail } from '@bugsnitch/shared';
import { formatDate } from './commit-row';

export function CommitInspection({ detail }: { detail: CommitDetail }) {
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
      <pre className="message">{detail.commit.message}</pre>
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
