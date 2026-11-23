// SPDX-License-Identifier: MPL-2.0
import { useState, type FormEvent } from "react";
import {
  type State,
  type Attempt,
  type Project,
  enc,
  post,
  request,
  messageOf,
} from "./api";
import { Badge, Field, Modal, Panel, when, human } from "./ui";
import type { Run } from "./actions";

export function DispatchControl({
  workspace,
  locked,
  run,
}: {
  workspace: State["workspace"];
  locked: boolean;
  run: Run;
}) {
  const [intent, setIntent] = useState<{
    stopped: boolean;
    version: number;
    requestId: string;
  } | null>(null);
  if (!workspace.dispatchVersion) return null;
  return (
    <Panel
      title="Emergency dispatch control"
      action={
        <Badge tone={workspace.stopped ? "warn" : "neutral"}>
          {workspace.stopped ? "Workspace stopped" : "Workspace enabled"}
        </Badge>
      }
    >
      <div className="panel-body">
        <p>
          {workspace.stopped
            ? "Unlock the workspace, then request a resume decision for each project you intend to restart."
            : "Stop new assignments across this workspace and request termination of active attempts. Disconnected computers report their state when they reconnect."}
        </p>
        <button
          disabled={locked}
          onClick={() =>
            setIntent({
              stopped: !workspace.stopped,
              version: workspace.dispatchVersion,
              requestId: crypto.randomUUID(),
            })
          }
        >
          {workspace.stopped
            ? "Review workspace unlock"
            : "Review emergency stop"}
        </button>
      </div>
      {intent && (
        <Modal
          title={
            intent.stopped
              ? "Stop workspace dispatch?"
              : "Unlock workspace dispatch?"
          }
          close={() => setIntent(null)}
        >
          <p>
            {intent.stopped
              ? "All projects will pause. Reachable computers receive stop requests. Check each attempt’s acknowledgment to see whether it stopped."
              : "Project dispatch remains paused until the owner approves a resume decision for each project."}
          </p>
          <div className="form-footer">
            <button onClick={() => setIntent(null)}>Cancel</button>
            <button
              className="primary"
              disabled={locked || workspace.dispatchVersion !== intent.version}
              onClick={async () => {
                if (
                  await run(
                    () => post("workspace/dispatch", intent),
                    intent.stopped
                      ? "Workspace stopped. Check attempt acknowledgments for termination."
                      : "Workspace unlocked. Projects are still paused.",
                  )
                )
                  setIntent(null);
              }}
            >
              {intent.stopped ? "Stop workspace" : "Unlock workspace"}
            </button>
          </div>
        </Modal>
      )}
    </Panel>
  );
}

export function SharedDiff({ attempt }: { attempt: Attempt }) {
  const metadata = attempt.sharedDiff;
  if (!metadata)
    return (
      <Panel title="Candidate diff">
        <p className="panel-body">
          No diff shared for this candidate. Inspect the local worktree on{" "}
          {attempt.machine}.
        </p>
      </Panel>
    );
  return (
    <DiffArtifact
      key={attempt.id + metadata.digest + metadata.candidateSha}
      attemptId={attempt.id}
      metadata={metadata}
    />
  );
}

function DiffArtifact({
  attemptId,
  metadata,
}: {
  attemptId: string;
  metadata: NonNullable<Attempt["sharedDiff"]>;
}) {
  const [text, setText] = useState<string | null>(null);
  const [loading, setLoading] = useState(false),
    [error, setError] = useState("");
  async function expand() {
    setLoading(true);
    setError("");
    try {
      const artifact = await request<typeof metadata & { text: string }>(
        "attempts/" + enc(attemptId) + "/diff",
      );
      if (
        artifact.digest !== metadata.digest ||
        artifact.baseSha !== metadata.baseSha ||
        artifact.candidateSha !== metadata.candidateSha
      )
        throw new Error(
          "The shared diff changed. Refresh the attempt to inspect the current artifact.",
        );
      setText(artifact.text);
    } catch (err) {
      setError(messageOf(err));
    } finally {
      setLoading(false);
    }
  }
  return (
    <Panel title="Shared candidate diff">
      <div className="panel-body">
        <p>
          Shared {when(metadata.sharedAt)} · Base{" "}
          <code>{metadata.baseSha}</code> · Candidate{" "}
          <code>{metadata.candidateSha}</code>
        </p>
        <button
          aria-expanded={text !== null}
          disabled={loading}
          onClick={() => {
            if (text === null) void expand();
            else setText(null);
          }}
        >
          {loading
            ? "Loading diff…"
            : text === null
              ? "Load shared diff"
              : "Close diff"}
        </button>
        {error && (
          <p className="alert" role="alert">
            {error}
          </p>
        )}
        {text !== null && (
          <pre
            tabIndex={0}
            role="region"
            aria-label="Shared source diff. Scroll to inspect long lines."
            className="diff"
          >
            <code>
              {text.split("\n").map((line, i) => (
                <span
                  className={
                    line.startsWith("@@")
                      ? "hunk"
                      : line.startsWith("+")
                        ? "addition"
                        : line.startsWith("-")
                          ? "deletion"
                          : ""
                  }
                  key={i}
                >
                  {line + "\n"}
                </span>
              ))}
            </code>
          </pre>
        )}
      </div>
    </Panel>
  );
}
export function DraftPR({
  attempt,
  project,
  state,
  locked,
  run,
}: {
  attempt: Attempt;
  project?: Project;
  state: State;
  locked: boolean;
  run: Run;
}) {
  const [draft, setDraft] = useState<{
    target: string;
    summary: string;
    behavior: string;
    version: number;
    policyVersion: number;
  } | null>(null);
  const current = (state.draftPRs || []).find(
    (d) =>
      d.attemptId === attempt.id && d.candidateSha === attempt.candidateSha,
  );
  const previous = (state.draftPRs || []).filter(
    (d) => d.attemptId === attempt.id && d !== current,
  );
  function prepare(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!project) return;
    const f = new FormData(e.currentTarget);
    setDraft({
      target: String(f.get("target")).trim(),
      summary: String(f.get("summary")).trim(),
      behavior: String(f.get("behavior")).trim(),
      version: attempt.version,
      policyVersion: project.policyVersion,
    });
  }
  return (
    <Panel title="Draft pull request">
      <div className="panel-body">
        {current ? (
          <>
            <Badge>{human(current.status)}</Badge>
            <p>{current.reason || "Target branch · " + current.target}</p>
            {current.number && project && (
              <a
                href={
                  "https://github.com/" +
                  project.repository +
                  "/pull/" +
                  current.number
                }
              >
                Open pull request #{current.number}
              </a>
            )}
          </>
        ) : attempt.state === "completed" && project?.allowDraftPR ? (
          <form onSubmit={prepare}>
            <p>
              Push the reviewed candidate to <code>bugsnitch/{attempt.id}</code>{" "}
              in {project.repository} before requesting a draft.
            </p>
            <Field title="Target branch">
              <input name="target" required placeholder="main" />
            </Field>
            <Field title="Change summary">
              <textarea name="summary" maxLength={1500} />
            </Field>
            <Field title="Resulting behavior">
              <textarea name="behavior" maxLength={1500} />
            </Field>
            <button disabled={locked}>Review draft request</button>
          </form>
        ) : (
          <p>
            A locally validated candidate, matching independent review and
            permission to open a draft PR are required.
          </p>
        )}
        {previous.map((d) => (
          <p key={d.candidateSha + d.target}>
            Earlier candidate <code>{d.candidateSha}</code> · {human(d.status)}{" "}
            · {d.target}
          </p>
        ))}
      </div>
      {draft && project && (
        <Modal title="Request a GitHub draft?" close={() => setDraft(null)}>
          <p>
            Create a draft pull request from the reviewed candidate into{" "}
            <strong>{draft.target}</strong> in {project.repository}.
          </p>
          <p>
            Candidate <code>{attempt.candidateSha}</code>
          </p>
          <div className="form-footer">
            <button onClick={() => setDraft(null)}>Cancel</button>
            <button
              className="primary"
              disabled={
                locked ||
                project.policyVersion !== draft.policyVersion ||
                attempt.version !== draft.version
              }
              onClick={async () => {
                if (
                  await run(
                    () =>
                      post(
                        "projects/" +
                          enc(project.id) +
                          "/attempts/" +
                          enc(attempt.id) +
                          "/draft-pr",
                        draft,
                      ),
                    "Draft PR request accepted. Check the reported GitHub result.",
                  )
                )
                  setDraft(null);
              }}
            >
              Request draft PR
            </button>
          </div>
        </Modal>
      )}
    </Panel>
  );
}
