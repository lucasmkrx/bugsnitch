// SPDX-License-Identifier: MPL-2.0
import { useEffect, useMemo, useReducer, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { CommitInspection, CommitRow, InvestigationCard } from '@bugsnitch/ui';
import { isHostMessage } from '@bugsnitch/shared';
import type { WebviewMessage } from '@bugsnitch/shared';
import { createCommitGraph } from '@bugsnitch/graph';
import { GraphLanes } from './graph-lanes';
import { initialViewState, reduceViewState } from './view-state';
import { HistoryPicker } from './history-picker';
import { RegressionPanel } from './regression-panel';
import './style.css';

declare function acquireVsCodeApi(): {
  postMessage(message: WebviewMessage): void;
  getState(): { scroll?: number } | undefined;
  setState(state: { scroll: number }): void;
};
const vscode = acquireVsCodeApi();
const send = (message: WebviewMessage) => vscode.postMessage(message);

function App({ logo }: { logo: string }) {
  const [query, setQuery] = useState('');
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
    const savedScroll = vscode.getState()?.scroll;
    const restore = () => {
      if (savedScroll !== undefined) window.scrollTo(0, savedScroll);
    };
    window.addEventListener('bugsnitch-restored', restore, { once: true });
    const saveScroll = () => vscode.setState({ scroll: window.scrollY });
    window.addEventListener('scroll', saveScroll, { passive: true });
    send({ version: 1, type: 'ready' });
    return () => {
      window.removeEventListener('message', receive);
      window.removeEventListener('scroll', saveScroll);
      window.removeEventListener('bugsnitch-restored', restore);
    };
  }, []);
  useEffect(() => {
    if (repository && !busy)
      window.dispatchEvent(new Event('bugsnitch-restored'));
  }, [repository, busy]);
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
          <img src={logo} alt="Bugsnitch" width="56" height="46" />
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
          <button
            className="secondary"
            disabled={busy}
            onClick={() => send({ version: 1, type: 'refresh' })}
          >
            Retry history
          </button>
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
      <RegressionPanel
        session={state.session}
        selectedHash={selectedHash}
        busy={busy}
        send={send}
        inspect={inspect}
      />
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
          {!commits.length && !busy && repository && !error && (
            <p className="empty">
              {repository?.head
                ? 'No commits to display.'
                : 'No commits yet. Your first commit starts the trail.'}
            </p>
          )}
          <label htmlFor="commit-search">Find a loaded commit</label>
          <input
            id="commit-search"
            type="search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Message, author, hash or ref"
          />
          {query.trim() && (
            <div
              className="search-results"
              role="region"
              aria-label="Loaded commit search results"
            >
              <p className="hint">
                Search covers {commits.length} loaded commits. Load more to
                expand it; the ancestry graph stays complete.
              </p>
              <ol>
                {commits
                  .filter((commit) =>
                    [commit.message, commit.author, commit.hash, ...commit.refs]
                      .join(' ')
                      .toLocaleLowerCase()
                      .includes(query.trim().toLocaleLowerCase()),
                  )
                  .slice(0, 30)
                  .map((commit) => (
                    <li key={commit.hash}>
                      <button
                        className="secondary"
                        disabled={busy}
                        onClick={() => inspect(commit.hash)}
                      >
                        {commit.shortHash} · {commit.subject}
                      </button>
                    </li>
                  ))}
              </ol>
              <p role="status">
                {
                  commits.filter((commit) =>
                    [commit.message, commit.author, commit.hash, ...commit.refs]
                      .join(' ')
                      .toLocaleLowerCase()
                      .includes(query.trim().toLocaleLowerCase()),
                  ).length
                }{' '}
                matches; showing up to 30.
              </p>
            </div>
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
              onCopyHash={(hash) =>
                send({ version: 1, type: 'copyHash', hash })
              }
              onMarkGood={(hash) =>
                send({ version: 1, type: 'markGood', hash })
              }
              onMarkBad={(hash) => send({ version: 1, type: 'markBad', hash })}
              onCompareParent={(parent) =>
                send({
                  version: 1,
                  type: 'compareParent',
                  hash: detail.commit.hash,
                  parent,
                })
              }
              onFileAction={(path, action) =>
                send({
                  version: 1,
                  type: (
                    {
                      patch: 'inspectFile',
                      diff: 'openDiff',
                      history: 'fileHistory',
                      copy: 'copyPath',
                    } as const
                  )[action],
                  hash: detail.commit.hash,
                  path,
                })
              }
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
      {state.fileHistory && (
        <section aria-labelledby="file-history-title">
          <h2 id="file-history-title">
            File history · {state.fileHistory.path}
          </h2>
          <p className="hint">
            Up to 100 commits from {state.fileHistory.tip.slice(0, 8)},
            following Git's rename history.
          </p>
          {state.fileHistory.hasMore && (
            <p role="status">
              History is limited to the newest 100 matching commits.
            </p>
          )}
          <ol className="candidate-list">
            {state.fileHistory.commits.map((commit) => (
              <li key={commit.hash}>
                <button
                  className="secondary"
                  disabled={busy}
                  onClick={() => inspect(commit.hash)}
                >
                  {commit.shortHash} · {commit.subject}
                </button>
              </li>
            ))}
          </ol>
        </section>
      )}
      <footer>
        Local ancestry graph · Lines connect commits to their parents.
      </footer>
    </main>
  );
}

const root = document.getElementById('root');
if (root) createRoot(root).render(<App logo={root.dataset.logo ?? ''} />);
