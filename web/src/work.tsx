// SPDX-License-Identifier: MPL-2.0
import { useState, type FormEvent } from "react";
import {
  enc,
  post,
  type Attempt,
  type Pull,
  type State,
  type Ticket,
} from "./api";
import { Field, Panel, human, link, when } from "./ui";
import type { Run } from "./actions";

interface WorkAction {
  label: string;
  description?: string;
  command?: string;
  href?: string;
}

const waiting: Record<string, string> = {
  plan_review_required: "Review the implementation plan",
  owner_input_required: "Answer the runner’s question",
  review_correction: "Review the findings before continuing",
  recovered_candidate: "Validate the recovered candidate",
  owner_paused: "Review the paused attempt",
};

const progressionLabels: Record<string, string> = {
  device_revoked: "Device access revoked",
  device_offline: "Reconnect the assigned runner",
  dispatch_paused: "Dispatch is paused",
  stage_running: "Wait for the current stage",
  review_plan: "Review the implementation plan",
  owner_input: "Answer the runner’s question",
  review_correction: "Review the findings before continuing",
  resume_candidate: "Validate the recovered candidate",
  inspect_execution: "Inspect the retained execution checkpoint",
  publish_draft: "Publish the owner-approved draft",
  approve_draft: "Review the candidate for publication",
  approve_correction: "Review the correction for publication",
  publish_correction: "Publish the owner-approved correction",
  wait_draft: "Wait for GitHub to create the draft",
  wait_inspection: "Wait for the first PR inspection",
  inspect_revision: "Review the new PR revision",
  wait_ci: "Wait for GitHub checks",
  prepare_signoff: "Review the PR and prepare sign-off",
  wait_deployment: "Review deployment evidence",
  deployment_handover: "Record deployment handover",
  hold: "Inspect the publication hold",
  finished: "Handover recorded",
  terminal: "Inspect the terminal attempt",
};

function observedAction(a: Attempt): WorkAction | undefined {
  const next = a.nextAction;
  if (!next || next.attemptId !== a.id || next.version !== a.version) return;
  const label = progressionLabels[next.kind];
  if (!label) return;
  const deployment = next.deployment?.[0];
  return {
    label:
      next.answer === "hold"
        ? "Revision held by the approver"
        : next.answer === "answered"
          ? "Answer recorded for the runner"
          : label,
    description:
      next.expiresAt && next.approver && !next.answer
        ? "Requested from " + next.approver + " until " + when(next.expiresAt)
        : next.reason,
    command:
      a.state === "waiting" || next.kind === "terminal"
        ? "bugsnitch local-show --id " + a.id
        : undefined,
    href: deployment
      ? link("incidents", a.projectId, deployment.incidentId)
      : [
            "wait_draft",
            "wait_inspection",
            "inspect_revision",
            "wait_ci",
            "prepare_signoff",
            "deployment_handover",
            "finished",
          ].includes(next.kind)
        ? link("prs", a.projectId)
        : undefined,
  };
}

export function attemptAction(state: State, a: Attempt): WorkAction {
  const observed = observedAction(a);
  const project = state.projects.find((p) => p.id === a.projectId);
  const draft = state.draftPRs?.find(
    (d) => d.attemptId === a.id && d.candidateSha === a.candidateSha,
  );
  if (
    observed &&
    !["prepare_signoff", "wait_ci"].includes(a.nextAction?.kind || "")
  )
    return observed;
  if (a.state === "completed" && draft?.number) {
    const pull = state.pullRequests.find(
      (p) =>
        p.projectId === a.projectId &&
        p.number === draft.number &&
        p.head === a.candidateSha &&
        p.issueNumber === a.issueNumber,
    );
    const githubAction = pull && githubPullAction(pull);
    if (githubAction)
      return {
        label: githubAction,
        href: `https://github.com/${a.repository}/pull/${draft.number}`,
      };
    if (observed)
      return pull?.signOff
        ? {
            label: "Review protected queue delivery",
            href: link("prs", a.projectId, String(pull.number)),
          }
        : { ...observed, href: link("prs", a.projectId, String(draft.number)) };
    if (pull?.state === "MERGED") {
      const incident = state.incidents.find(
        (i) => i.projectId === a.projectId && i.issueNumber === a.issueNumber,
      );
      return incident
        ? {
            label: "Review current deployment evidence",
            href: link("incidents", incident.projectId, incident.id),
          }
        : {
            label: pull.handovers?.length
              ? "Review deployment handover"
              : "Record deployment handover",
            href: link("prs", a.projectId),
          };
    }
    return {
      label:
        pull?.checks === "PENDING"
          ? "Wait for GitHub checks"
          : "Review the pull request",
      href: link("prs", a.projectId),
    };
  }
  if (observed) return observed;
  if (a.state === "failed" || a.state === "cancelled") {
    return {
      label: "Inspect the terminal attempt",
      command: "bugsnitch local-show --id " + a.id,
    };
  }
  if (a.control && ["requested", "received"].includes(a.control.status)) {
    return {
      label: "Wait for the runner to acknowledge " + a.control.operation,
    };
  }
  if (a.state !== "completed" && a.connection === "disconnected") {
    return {
      label: "Reconnect the assigned runner",
      description: "The recorded attempt stays assigned to " + a.machine + ".",
    };
  }
  if (state.workspace.stopped || project?.paused) {
    return {
      label: state.workspace.stopped
        ? "Workspace dispatch is stopped"
        : "Project dispatch is paused",
    };
  }
  if (a.state === "running") {
    return { label: "Wait for " + human(a.phase).toLowerCase() };
  }
  if (a.state === "waiting") {
    const code = a.reasonCode || "";
    return {
      label: waiting[code] || "Inspect the wait on the runner",
      description: code && !waiting[code] ? human(code) : undefined,
      command: "bugsnitch local-show --id " + a.id,
    };
  }
  if (a.state === "completed") {
    if (draft)
      return {
        label: "Check the draft request",
        description: draft.reason || human(draft.status),
      };
    if (project?.allowDraftPR)
      return { label: "Publish the candidate and request a draft PR" };
    return {
      label: "Review the local candidate",
      command: "bugsnitch local-show --id " + a.id,
    };
  }
  return { label: "Inspect the latest attempt report" };
}

export function githubPullAction(pull?: Pull) {
  if (pull?.state !== "OPEN") return;
  if (pull.draft) return "Mark draft ready for review on GitHub";
  if (pull.review === "CHANGES_REQUESTED")
    return "Review requested changes on GitHub";
  if (pull.review !== "APPROVED") return "Request a review on GitHub";
  if (!pull.signOff) return;
  if (pull.queueDelivery?.state === "awaiting_queue")
    return "Add this PR to the GitHub merge queue";
  if (
    !pull.queueDelivery &&
    pull.queue?.present === false &&
    !pull.queue.rules?.queueRequired
  )
    return "Open GitHub merge controls";
}

export function ticketAction(
  state: State,
  ticket: Ticket,
  attempt?: Attempt,
): WorkAction {
  if (attempt)
    return {
      label: attemptAction(state, attempt).label,
      href: link("work", attempt.projectId, attempt.id),
    };
  const project = state.projects.find((p) => p.id === ticket.projectId);
  if (state.workspace.stopped || project?.paused)
    return {
      label: state.workspace.stopped
        ? "Workspace dispatch is stopped"
        : "Project dispatch is paused",
    };
  if (ticket.reason) return { label: ticket.reason };
  if (ticket.state === "closed") return { label: "Review the closed issue" };
  if (ticket.state !== "open")
    return { label: "Confirm issue status on GitHub" };
  return {
    label: "Available for worker selection",
  };
}

export function AttemptAction({
  state,
  attempt,
  run,
  locked,
}: {
  state: State;
  attempt: Attempt;
  run: Run;
  locked: boolean;
}) {
  const [feedback, setFeedback] = useState("");
  const action = attemptAction(state, attempt);
  const canContinue =
    attempt.state === "waiting" &&
    attempt.connection === "connected" &&
    action.command !== undefined &&
    Object.hasOwn(waiting, attempt.reasonCode || "");
  const next =
    attempt.nextAction?.attemptId === attempt.id &&
    attempt.nextAction.version === attempt.version
      ? attempt.nextAction
      : undefined;
  const isCorrection = next?.kind === "approve_correction";
  const isQuestion =
    next?.kind === "owner_input" &&
    next.decisionKind === "owner_question" &&
    !!next.question;
  const remoteDecision =
    (next?.kind === "review_plan" || isCorrection || isQuestion) &&
    !!next.approver &&
    (!!next.expiresAt || !!next.answer);
  const canAnswer =
    next &&
    remoteDecision &&
    !next.answer &&
    next.expiresAt &&
    new Date(next.expiresAt).getTime() > Date.now() &&
    next.approver === state.viewer?.id &&
    (state.viewer?.role === "owner" ||
      (!isQuestion && state.viewer?.role === "approver"));
  const answerText = feedback.trim();
  const answerBytes = new TextEncoder().encode(answerText).length;
  function answer(choice: "approve" | "hold" | "answered") {
    if (
      locked ||
      !canAnswer ||
      (choice === "answered" && (!answerBytes || answerBytes > 2048))
    )
      return;
    return run(
      () =>
        post("attempts/" + enc(attempt.id) + "/ticket-decision", {
          version: attempt.version,
          answer: choice,
          ...(choice === "answered" ? { feedback: answerText } : {}),
        }),
      choice === "answered"
        ? "Answer recorded for the runner."
        : choice === "approve"
          ? "Action approved for this revision."
          : "Action held.",
    );
  }
  return (
    <div className="panel-body work-action">
      <h3>Next action</h3>
      <p>
        {action.href ? <a href={action.href}>{action.label}</a> : action.label}
      </p>
      {action.description && <p>{action.description}</p>}
      {action.command && !remoteDecision && (
        <p>
          <code>{action.command}</code>
        </p>
      )}
      {next?.kind === "review_plan" && next.planSummary && (
        <p className="worker-text">{next.planSummary}</p>
      )}
      {isQuestion && <p className="worker-text">{next.question}</p>}
      {next?.preparedEvidence && (
        <details>
          <summary>
            Prepared evidence for inspection{" "}
            {next.preparedEvidence.inspectionVersion}
          </summary>
          <p>
            Independent review{" "}
            <code>{next.preparedEvidence.independentReview}</code>
          </p>
          <p>
            Combined validation{" "}
            <code>{next.preparedEvidence.combinedValidation}</code>
          </p>
        </details>
      )}
      {canAnswer && isQuestion && (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            void answer("answered");
          }}
        >
          <Field title="Your answer">
            <textarea
              value={feedback}
              maxLength={2048}
              required
              onChange={(event) => setFeedback(event.target.value)}
            />
          </Field>
          {answerBytes > 2048 && (
            <p role="alert">Shorten the answer to 2,048 UTF-8 bytes.</p>
          )}
          <button disabled={locked || !answerBytes || answerBytes > 2048}>
            Send answer to runner
          </button>
        </form>
      )}
      {canAnswer && !isQuestion && (
        <div className="actions">
          <button
            type="button"
            disabled={locked}
            onClick={() => void answer("approve")}
          >
            {isCorrection
              ? "Approve correction publication"
              : "Approve reviewed plan"}
          </button>
          <button
            type="button"
            disabled={locked}
            onClick={() => void answer("hold")}
          >
            Hold this revision
          </button>
        </div>
      )}
      {canContinue && !remoteDecision && !next?.answer && (
        <p>
          After reviewing this checkpoint on {attempt.machine}, run{" "}
          <code>
            bugsnitch local-continue --id {attempt.id} --version{" "}
            {attempt.version}
            {attempt.reasonCode === "owner_input_required"
              ? ' --feedback "YOUR ANSWER"'
              : ""}
          </code>
          .
        </p>
      )}
    </div>
  );
}

export function LinearLinks({
  tickets,
  run,
  locked,
  canLink,
}: {
  tickets: Ticket[];
  run: Run;
  locked: boolean;
  canLink: boolean;
}) {
  const [choice, setChoice] = useState("");
  const distinct = new Map(
    tickets.map((t) => [t.projectId + ":" + t.number, t]),
  );
  const selected = distinct.get(choice);
  const linked = [...distinct.values()].filter((t) => t.linear);
  if (!linked.length && (!canLink || !distinct.size)) return null;
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!selected) return;
    const issueId =
      selected.linear?.id ||
      String(new FormData(e.currentTarget).get("issueId")).trim();
    await run(
      () =>
        post(
          "projects/" +
            enc(selected.projectId) +
            "/issues/" +
            selected.number +
            "/linear",
          { issueId },
        ),
      "Linear issue snapshot recorded.",
    );
  }
  return (
    <Panel title="Linear issues">
      <div className="panel-body">
        {linked.length > 0 && (
          <ul>
            {linked.map(
              (t) =>
                t.linear && (
                  <li key={t.id}>
                    <a href={t.linear.url}>
                      {t.linear.identifier} {t.linear.title}
                    </a>
                    {" · " +
                      t.repository +
                      " #" +
                      t.number +
                      " · " +
                      t.linear.state +
                      (t.linear.delegated ? " · Delegated to Bugsnitch" : "") +
                      " · Read " +
                      when(t.linear.checkedAt)}
                  </li>
                ),
            )}
          </ul>
        )}
        {canLink && (
          <form onSubmit={submit}>
            <Field title="Canonical GitHub issue">
              <select
                required
                value={choice}
                onChange={(e) => setChoice(e.target.value)}
              >
                <option value="">Choose a GitHub issue</option>
                {[...distinct].map(([key, t]) => (
                  <option key={key} value={key}>
                    {t.repository} #{t.number}
                  </option>
                ))}
              </select>
            </Field>
            {!selected?.linear && (
              <Field title="Linear issue identifier">
                <input
                  key={choice}
                  name="issueId"
                  placeholder="TEAM-123"
                  required
                  maxLength={60}
                />
              </Field>
            )}
            <button disabled={locked || !selected}>
              {selected?.linear ? "Refresh Linear issue" : "Link Linear issue"}
            </button>
          </form>
        )}
      </div>
    </Panel>
  );
}
