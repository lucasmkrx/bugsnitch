// SPDX-License-Identifier: MPL-2.0
import { useState } from 'react';
import type { InvestigationSession, WebviewMessage } from '@bugsnitch/shared';

export function RegressionPanel({
  session,
  selectedHash,
  busy,
  send,
  inspect,
}: {
  session: InvestigationSession;
  selectedHash: string | null;
  busy: boolean;
  send(message: WebviewMessage): void;
  inspect(hash: string): void;
}) {
  const selected = session.range?.commits.find(
    (commit) => commit.hash === selectedHash,
  );
  const annotation = session.annotations.find(
    (entry) => entry.hash === selectedHash,
  );
  return (
    <section className="regression" aria-labelledby="range-title">
      <details open={Boolean(session.good || session.bad)}>
        <summary>
          <h2 id="range-title">Regression investigation</h2>
        </summary>
        <p className="hint">
          Inspect commits and mark a known-good and known-bad revision to bound
          your investigation.
        </p>
        <dl className="facts">
          <dt>Known good</dt>
          <dd>
            {session.good
              ? `${session.good.shortHash} · ${session.good.subject}`
              : 'Choose from commit details'}
          </dd>
          <dt>Known bad</dt>
          <dd>
            {session.bad
              ? `${session.bad.shortHash} · ${session.bad.subject}`
              : 'Choose from commit details'}
          </dd>
        </dl>
        <div className="action-group">
          <button
            disabled={busy || !session.good || !session.bad}
            onClick={() => send({ version: 1, type: 'viewRange' })}
          >
            Investigate range
          </button>
          <button
            className="secondary"
            disabled={busy || (!session.good && !session.bad)}
            onClick={() => send({ version: 1, type: 'clearRange' })}
          >
            Clear investigation
          </button>
          <button
            className="secondary"
            disabled={busy || !session.range}
            onClick={() => send({ version: 1, type: 'exportInvestigation' })}
          >
            Export investigation JSON
          </button>
        </div>
        {session.range && (
          <>
            <p className="hint">
              Frozen range {session.range.good.slice(0, 8)} →{' '}
              {session.range.bad.slice(0, 8)}. {session.range.commits.length}{' '}
              commits loaded. Conclusions are your observations; Bugsnitch does
              not classify regressions automatically.
            </p>
            {session.range.hasMore && (
              <p role="status">
                Showing the newest 200 commits. Narrow the range for a complete
                candidate list.
              </p>
            )}
            <ol className="candidate-list">
              {session.range.commits.map((commit) => (
                <li key={commit.hash}>
                  <button
                    className="secondary"
                    disabled={busy}
                    aria-pressed={commit.hash === selectedHash}
                    onClick={() => inspect(commit.hash)}
                  >
                    <code>{commit.shortHash}</code> {commit.subject}
                  </button>
                  <span>
                    {session.annotations.find(
                      (entry) => entry.hash === commit.hash,
                    )?.verdict ?? 'Unreviewed'}
                  </span>
                </li>
              ))}
            </ol>
            {selected && (
              <EvidenceNote
                key={selected.hash}
                hash={selected.hash}
                annotation={annotation}
                busy={busy}
                send={send}
              />
            )}
            <p className="hint">
              Export includes commit metadata and your notes. Review them before
              sharing; repository paths and source patches are omitted.
            </p>
          </>
        )}
      </details>
    </section>
  );
}
function EvidenceNote({
  hash,
  annotation,
  busy,
  send,
}: {
  hash: string;
  annotation: InvestigationSession['annotations'][number] | undefined;
  busy: boolean;
  send(message: WebviewMessage): void;
}) {
  const [note, setNote] = useState(annotation?.note ?? '');
  const [verdict, setVerdict] = useState<
    'candidate' | 'confirmed' | 'excluded'
  >(annotation?.verdict ?? 'candidate');
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        send({ version: 1, type: 'annotate', hash, note, verdict });
      }}
    >
      <label htmlFor="evidence-verdict">Your assessment</label>
      <select
        id="evidence-verdict"
        value={verdict}
        disabled={busy}
        onChange={(event) => setVerdict(event.target.value as typeof verdict)}
      >
        <option value="candidate">Candidate</option>
        <option value="confirmed">Confirmed by my evidence</option>
        <option value="excluded">Excluded by my evidence</option>
      </select>
      <label htmlFor="evidence-note">Evidence and reproduction notes</label>
      <textarea
        id="evidence-note"
        rows={3}
        maxLength={4000}
        value={note}
        disabled={busy}
        onChange={(event) => setNote(event.target.value)}
      />
      <button disabled={busy} type="submit">
        Save assessment
      </button>
      <span role="status">
        {annotation ? 'Assessment saved in this panel.' : ''}
      </span>
    </form>
  );
}
