// SPDX-License-Identifier: MPL-2.0
import { useEffect, useMemo, useReducer } from 'react';
import { createRoot } from 'react-dom/client';
import { CommitInspection, CommitRow, InvestigationCard } from '@bugsnitch/ui';
import { isHostMessage } from '@bugsnitch/shared';
import type { WebviewMessage } from '@bugsnitch/shared';
import { createCommitGraph } from '@bugsnitch/graph';
import { GraphLanes } from './graph-lanes';
import { initialViewState, reduceViewState } from './view-state';
import { HistoryPicker } from './history-picker';
import './style.css';

declare function acquireVsCodeApi(): {
  postMessage(message: WebviewMessage): void;
};
const vscode = acquireVsCodeApi();
const send = (message: WebviewMessage) => vscode.postMessage(message);

function App({ logo }: { logo: string }) {
  const [state, dispatch] = useReducer(reduceViewState, initialViewState);
  const {
    repository,
    commits,
    investigation,
    detail,
    busy,
    hasMore,
    error,
    selectedHash,
    anchor,
    revealHash,
    references,
    returnAnchor,
  } = state;
  useEffect(() => {
    const receive = (event: MessageEvent<unknown>) => {
      const message = event.data;
      if (!isHostMessage(message)) return;
      dispatch({ type: 'host', message });
    };
    window.addEventListener('message', receive);
    send({ version: 1, type: 'ready' });
    return () => window.removeEventListener('message', receive);
  }, []);
  useEffect(() => {
    if (detail) document.getElementById('commit-title')?.focus();
  }, [detail]);
  useEffect(() => {
    if (!revealHash) return;
    const row = document.getElementById(`commit-${revealHash}`);
    if (row && !busy) {
      row.scrollIntoView({ block: 'center' });
      row.focus({ preventScroll: true });
      dispatch({ type: 'revealed' });
    }
  }, [commits, revealHash, busy]);
  const inspect = (hash: string) => {
    dispatch({ type: 'select', hash });
    send({ version: 1, type: 'inspectCommit', hash });
  };
  const showInGraph = (hash: string) => {
    dispatch({ type: 'reveal', hash });
    if (!commits.some((commit) => commit.hash === hash))
      send({ version: 1, type: 'focusCommit', hash });
  };
  const graph = useMemo(() => createCommitGraph(commits), [commits]);
  return (
    <main>
      <header className="masthead">
        <div className="identity">
          <img src={logo} alt="Bugsnitch" width="94" height="76" />
          <div>
            <h1>Find what changed.</h1>
            <p>Find where the bug started.</p>
          </div>
        </div>
        <button
          className="secondary"
          disabled={busy}
          onClick={() => send({ version: 1, type: 'refresh' })}
        >
          Refresh history
        </button>
      </header>
      <p className="local-note">
        Local Git forensics <span aria-hidden="true">/</span> Read-only{' '}
        <span aria-hidden="true">/</span> No telemetry
      </p>
      <div className="activity" role="status" aria-live="polite">
        {busy ? 'Reading local Git history…' : ''}
      </div>
      {error && (
        <div role="alert" className="error">
          {error}
        </div>
      )}
      {repository && (
        <section className="repository" aria-label="Repository overview">
          <div>
            <p className="eyebrow">Repository</p>
            <h2>{repository.name}</h2>
            <p className="path">{repository.root}</p>
          </div>
          <div className="repository-state">
            <strong>{repository.branch}</strong>
            <p>
              {Object.values(repository.status).every((count) => count === 0)
                ? 'Working tree clean'
                : `${repository.status.staged} staged · ${repository.status.modified} modified · ${repository.status.untracked} untracked · ${repository.status.conflicted} conflicted`}
            </p>
            {repository.filtersDisabled && (
              <p className="hint">
                Content filters bypassed for safety; modified counts may differ
                from Git.
              </p>
            )}
          </div>
        </section>
      )}
      {investigation && (
        <InvestigationCard
          investigation={investigation}
          disabled={busy}
          onInspect={inspect}
        />
      )}
      <div className="workspace">
        <section
          className="history"
          aria-labelledby="history-title"
          aria-busy={busy}
        >
          <div className="section-heading">
            <h2 id="history-title">Commit graph</h2>
            <span className="count">{graph.nodes.length} loaded</span>
          </div>
          {anchor && (
            <HistoryPicker
              anchor={anchor}
              references={references}
              disabled={busy}
              onSelect={(name) =>
                send(
                  name === null
                    ? { version: 1, type: 'returnToHead' }
                    : { version: 1, type: 'selectRef', name },
                )
              }
            />
          )}
          {anchor && (
            <div className="history-context">
              <p className="hint">
                Graph from {anchor.label}. The working tree stays on{' '}
                {repository?.branch}.
              </p>
              {anchor.kind === 'commit' && returnAnchor && (
                <button
                  className="secondary"
                  disabled={busy}
                  onClick={() => send({ version: 1, type: 'returnToHistory' })}
                >
                  Return to {returnAnchor.label} history
                </button>
              )}
            </div>
          )}
          {selectedHash &&
            !commits.some((commit) => commit.hash === selectedHash) && (
              <p className="selection-note">
                Selected commit <code>{selectedHash.slice(0, 8)}</code> is
                outside this loaded graph.{' '}
                <button
                  className="secondary"
                  disabled={busy}
                  onClick={() => showInGraph(selectedHash)}
                >
                  Show selected commit in graph
                </button>
              </p>
            )}
          {!commits.length && !busy && (
            <p className="empty">
              {repository?.head
                ? 'No commits to display.'
                : 'No commits yet. Your first commit starts the trail.'}
            </p>
          )}
          <ol className="commit-list">
            {commits.map((commit, row) => (
              <CommitRow
                key={commit.hash}
                commit={commit}
                selected={selectedHash === commit.hash}
                disabled={busy}
                onInspect={inspect}
                graph={
                  <GraphLanes
                    node={graph.nodes[row]!}
                    laneCount={graph.laneCount}
                    boundary={
                      row === commits.length - 1 && graph.boundaries.length > 0
                    }
                  />
                }
              />
            ))}
          </ol>
          {graph.boundaries.length > 0 && (
            <p className="hint graph-boundary">
              Dashed lanes continue to parents outside the loaded history.
            </p>
          )}
          {hasMore && (
            <button
              className="secondary load-more"
              disabled={busy}
              onClick={() => send({ version: 1, type: 'loadMore' })}
            >
              Load more commits
            </button>
          )}
        </section>
        <aside className="inspection" aria-label="Investigation details">
          {detail ? (
            <CommitInspection
              detail={detail}
              disabled={busy}
              onInspect={inspect}
              onShowInGraph={showInGraph}
            />
          ) : (
            <div className="empty">
              <p className="eyebrow">Follow the evidence</p>
              <h2>Every change has a history.</h2>
              <p>Select a commit to inspect its message, files, and patch.</p>
              <p>
                In your editor, run{' '}
                <strong>Bugsnitch: Snitch on Current Line</strong> to trace a
                saved line to its origin.
              </p>
            </div>
          )}
        </aside>
      </div>
      <footer>
        Local ancestry graph · Lines connect commits to their parents.
      </footer>
    </main>
  );
}

const root = document.getElementById('root');
if (root) createRoot(root).render(<App logo={root.dataset.logo ?? ''} />);
