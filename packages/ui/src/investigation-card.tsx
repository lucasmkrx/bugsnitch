// SPDX-License-Identifier: MPL-2.0
import type { Investigation } from '@bugsnitch/shared';
import { formatDate } from './commit-row';

export function InvestigationCard({
  investigation,
  disabled,
  onInspect,
}: {
  investigation: Investigation;
  disabled: boolean;
  onInspect: (hash: string) => void;
}) {
  const line = investigation.provenance;
  return (
    <section className="investigation" aria-labelledby="investigation-title">
      <p className="eyebrow">Line provenance</p>
      <h2 id="investigation-title">
        {line.uncommitted
          ? 'This line has local changes'
          : 'Where this line started'}
      </h2>
      <p className="path">
        {line.path}:{line.currentLine}
      </p>
      {line.uncommitted ? (
        <p>
          Git cannot attribute this saved line to a commit yet. Investigate an
          unchanged line to find its origin.
        </p>
      ) : (
        <>
          <h3>{investigation.commit?.subject || line.subject}</h3>
          <dl className="facts">
            <dt>Commit</dt>
            <dd>
              <code>{line.hash}</code>
            </dd>
            <dt>Author</dt>
            <dd>{line.author}</dd>
            <dt>Author date</dt>
            <dd>{formatDate(line.authorDate)}</dd>
            <dt>Original location</dt>
            <dd>
              {line.originalPath}:{line.originalLine}
            </dd>
            <dt>Current line</dt>
            <dd>{line.currentLine}</dd>
          </dl>
          <button disabled={disabled} onClick={() => onInspect(line.hash)}>
            Inspect this commit
          </button>
        </>
      )}
    </section>
  );
}
