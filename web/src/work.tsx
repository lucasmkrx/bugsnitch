// SPDX-License-Identifier: MPL-2.0
import { useState, type FormEvent } from "react";
import { enc, post, type Attempt, type State, type Ticket } from "./api";
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

export function attemptAction(state: State, a: Attempt): WorkAction {
  const project = state.projects.find((p) => p.id === a.projectId);
  const draft = state.draftPRs?.find(
    (d) => d.attemptId === a.id && d.candidateSha === a.candidateSha,
  );
  if (a.state === "completed" && draft?.number) {
    const pull = state.pullRequests.find(
      (p) =>
        p.projectId === a.projectId &&
        p.number === draft.number &&
        p.head === a.candidateSha &&
        p.issueNumber === a.issueNumber,
    );
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
    label:
      ticket.source === "plan"
        ? "Check eligibility on the runner"
        : "Review the issue for local work",
    description:
      ticket.source === "plan"
        ? "The runner checks dependencies, capacity, and the current GitHub issue before admission."
        : undefined,
  };
}

export function AttemptAction({
  state,
  attempt,
}: {
  state: State;
  attempt: Attempt;
}) {
  const action = attemptAction(state, attempt);
  const canContinue =
    attempt.state === "waiting" &&
    attempt.connection === "connected" &&
    action.command !== undefined &&
    Object.hasOwn(waiting, attempt.reasonCode || "");
  return (
    <div className="panel-body work-action">
      <h3>Next action</h3>
      <p>
        {action.href ? <a href={action.href}>{action.label}</a> : action.label}
      </p>
      {action.description && <p>{action.description}</p>}
      {action.command && (
        <p>
          <code>{action.command}</code>
        </p>
      )}
      {canContinue && (
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
