// SPDX-License-Identifier: MPL-2.0
export interface Policy {
  requirePlan: boolean;
  allowPublication: boolean;
  allowPromotion: boolean;
  allowDraftPR: boolean;
  autoTriage: boolean;
}
export interface Project extends Policy {
  githubConnected?: boolean | null;
  retentionDays: number;
  preset: string;
  policyVersion: number;
  id: string;
  name: string;
  repository: string;
  paused: boolean;
  version: number;
}
export interface Device {
  id: string;
  turnLimit?: number;
  name: string;
  platform: string;
  projects: string[];
  lastContact: string | null;
  revoked: boolean;
}
export interface Incident {
  id: string;
  projectId: string;
  observed: string;
  title?: string;
  expected: string;
  steps: string;
  source: string;
  status: string;
  version: number;
  firstSeen: string;
  lastSeen: string;
  issueNumber?: number;
  occurrences: number;
  promotion?: string;
  promotionReason?: string;
  environment?: string;
  release?: string;
  issueState?: string;
  frames?: unknown[];
}
export interface Decision {
  id: string;
  projectId: string;
  subjectVersion: number;
  version: number;
  status: string;
  question: string;
  options: string[];
  answer: string | null;
  requestId: string | null;
  expiresAt: string;
}
export interface StageEvidence {
  durationMs?: number;
  phase: string;
  candidateSha?: string;
  changedFiles: number;
  exitCode?: number;
  importantFindings: number;
  minorFindings: number;
}
export interface Attempt {
  nextAction?: {
    attemptId: string;
    version: number;
    kind: string;
    reason?: string;
    disposition: "act" | "wait" | "ask" | "finish";
    approver?: string;
    expiresAt?: string;
    decisionKind?: string;
    question?: string;
    planSummary?: string;
    answer?: "approve" | "hold" | "answered";
    deployment?: { incidentId: string; environment: string; held: boolean }[];
    preparedEvidence?: {
      inspectionVersion: number;
      independentReview: string;
      combinedValidation: string;
    };
  } | null;
  reasonCode?: string;
  sharedDiff?: {
    digest: string;
    candidateSha: string;
    baseSha: string;
    sharedAt: string;
  } | null;
  evidence?: StageEvidence[];
  id: string;
  projectId: string;
  deviceId?: string;
  machine: string;
  repository: string;
  issueNumber: number;
  phase: string;
  state: string;
  baseSha: string;
  candidateSha: string | null;
  version: number;
  updatedAt: string;
  connection: string;
  control?: { id: string; operation: string; status: string } | null;
}
export interface Ticket {
  id: string;
  projectId: string;
  number: number;
  repository: string;
  title: string;
  state: string;
  source?: string;
  reason?: string;
  linear?: {
    delegated?: boolean;
    id: string;
    identifier: string;
    title: string;
    url: string;
    state: string;
    checkedAt: string;
  } | null;
}
export interface Message {
  id: string;
  projectId: string;
  body: string;
  reason: string;
  status: string;
  createdAt: string;
}
export interface Activity {
  seq: number;
  projectId: string | null;
  kind: string;
  subjectId: string;
  createdAt: string;
}
export interface PlanTicket {
  number: number;
  title: string;
  criteria: string[] | null;
  verification: string;
  priority: number;
  estimateHours: number;
  dependencies: number[] | null;
  resources: string[] | null;
}
export interface PlanDoc {
  title: string;
  milestone: string;
  target?: string;
  forecast?: string;
  assumptions: string;
  tickets: PlanTicket[] | null;
}
export interface ProgressForecast {
  status: "unknown" | "projected" | "complete";
  completedTickets: number;
  totalTickets: number;
  completedEstimateHours: number;
  totalEstimateHours: number;
  asOf: string;
  date?: string;
  targetDeltaDays?: number;
  reason?: string;
}
export interface Plan {
  progressForecast?: ProgressForecast;
  id: string;
  projectId: string;
  title: string;
  version: number;
  status: string;
  document: PlanDoc;
  executionOrder: number[];
}
export interface Pull {
  projectId: string;
  number: number;
  issueNumber: number;
  title: string;
  head: string;
  base: string;
  target: string;
  criteria: string;
  review: string;
  checks: string;
  checkCount: number;
  state: string;
  version: number;
  observedAt: string;
  issueState?: string;
  draft?: boolean;
  queue?: { present: boolean; rules?: { queueRequired: boolean } };
  signOff?: { at: string } | null;
  queueDelivery?: {
    state: string;
    reason: string;
    groupSha: string;
    version: number;
    head: string;
    base: string;
    target: string;
    environment: string;
    expiresAt: string;
  };
  blockers: string[];
  autoMerge: boolean;
  watching?: boolean;
  validation?: string;
  independentReview?: string;
  mergedSha?: string;
  handovers?:
    | {
        mergedSha: string;
        environment: string;
        build: string;
        evidenceDigest: string;
        summary: string;
        testSteps: string;
        recordedAt: string;
        recordedBy: string;
        cost?: {
          admittedTurns: number;
          meteredTurns: number;
          pricedTurns: number;
          inputTokens: number;
          outputTokens: number;
          estimatedUSD?: number;
        };
      }[]
    | null;
}
export interface Publication {
  id: string;
  projectId: string;
  version: number;
  status: string;
  document: PlanDoc;
  planId?: string;
  issues: {
    localNumber: number;
    number?: number;
    status: string;
    reason?: string;
  }[];
}
export interface Draft {
  approvalVersion?: number;
  reportVersion?: number;
  policyVersion?: number;
  projectId: string;
  attemptId: string;
  candidateSha: string;
  baseSha: string;
  target: string;
  status: string;
  number?: number;
  reason?: string;
  updatedAt: string;
}
export interface State {
  viewer?: {
    id?: string;
    name: string;
    role: "owner" | "operator" | "approver";
    projectId?: string | null;
  };
  draftPRs: Draft[];
  publications: Publication[];
  releases?: {
    id: string;
    name: string;
    environment: string;
    cancelledAt: string | null;
    stages: {
      projectId: string;
      number: number;
      status: string;
      head: string;
      target: string;
    }[];
  }[];
  budget?: { monthlyTurns: number | null; usedTurns: number } | null;
  limits: { tickets: number; turns: number; version: number };
  workspace: {
    id: string;
    name: string;
    stopped: boolean;
    dispatchVersion: number;
  };
  projects: Project[];
  devices: Device[];
  incidents: Incident[];
  decisions: Decision[];
  attempts: Attempt[];
  tickets: Ticket[];
  plans: Plan[];
  pullRequests: Pull[];
  messages: Message[];
  events: Activity[];
  capabilities: { demo?: boolean; hostedInference: boolean; reason: string };
  updatedAt: string;
}

export class HttpError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}
export async function request<T>(
  path: string,
  init: RequestInit = {},
): Promise<T> {
  let response: Response;
  try {
    response = await fetch("/api/v1/" + path, {
      credentials: "same-origin",
      cache: "no-store",
      ...init,
    });
  } catch {
    throw new Error(
      "The service could not be reached. Refresh to check the latest state.",
    );
  }
  if (!response.ok) {
    const error = await response.json().catch(() => null);
    throw new HttpError(
      response.status,
      error?.error || "The service returned HTTP " + response.status,
    );
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}
export const readState = () => request<State>("state");
export const post = <T = Record<string, unknown>>(
  path: string,
  body: unknown,
) =>
  request<T>(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
export const enc = encodeURIComponent;
export const messageOf = (error: unknown) =>
  error instanceof Error ? error.message : "The request failed.";
