// SPDX-License-Identifier: MPL-2.0
import type {
  Commit,
  CommitDetail,
  HistoryAnchor,
  HostMessage,
  Investigation,
  RepositorySnapshot,
  ReferencePage,
  FileHistory,
  InvestigationSession,
} from '@bugsnitch/shared';

export interface ViewState {
  repository: RepositorySnapshot | null;
  commits: Commit[];
  anchor: HistoryAnchor | null;
  returnAnchor: HistoryAnchor | null;
  references: ReferencePage;
  investigation: Investigation | null;
  detail: CommitDetail | null;
  selectedHash: string | null;
  revealHash: string | null;
  busy: boolean;
  hasMore: boolean;
  error: string;
  fileHistory: FileHistory | null;
  session: InvestigationSession;
}
export const initialViewState: ViewState = {
  repository: null,
  commits: [],
  anchor: null,
  returnAnchor: null,
  references: { references: [], truncated: false },
  investigation: null,
  detail: null,
  selectedHash: null,
  revealHash: null,
  busy: true,
  hasMore: false,
  error: '',
  fileHistory: null,
  session: { good: null, bad: null, range: null, annotations: [] },
};
export type ViewAction =
  | { type: 'host'; message: HostMessage }
  | { type: 'select'; hash: string }
  | { type: 'reveal'; hash: string }
  | { type: 'revealed' };

export function reduceViewState(
  state: ViewState,
  action: ViewAction,
): ViewState {
  if (action.type === 'select')
    return {
      ...state,
      selectedHash: action.hash,
      detail: state.detail?.commit.hash === action.hash ? state.detail : null,
    };
  if (action.type === 'reveal') return { ...state, revealHash: action.hash };
  if (action.type === 'revealed') return { ...state, revealHash: null };
  const message = action.message;
  switch (message.type) {
    case 'fileHistory':
      return { ...state, fileHistory: message.history, error: '' };
    case 'session':
      return { ...state, session: message.session, error: '' };
    case 'history':
      return {
        ...state,
        repository: message.repository,
        anchor: message.anchor,
        returnAnchor: message.returnAnchor,
        references: message.references,
        commits: message.append
          ? [...state.commits, ...message.page.commits]
          : message.page.commits,
        hasMore: message.page.hasMore,
        error: '',
      };
    case 'investigation': {
      const hash = message.investigation.commit?.hash ?? null;
      return {
        ...state,
        investigation: message.investigation,
        selectedHash: hash,
        detail: state.detail?.commit.hash === hash ? state.detail : null,
        error: '',
      };
    }
    case 'commit':
      return {
        ...state,
        detail: message.detail,
        selectedHash: message.detail.commit.hash,
        error: '',
      };
    case 'busy':
      return { ...state, busy: message.busy };
    case 'error':
      return { ...state, error: message.message, revealHash: null };
  }
}
