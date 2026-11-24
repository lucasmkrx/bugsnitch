// SPDX-License-Identifier: MPL-2.0
import { useState, type FormEvent } from "react";
import {
  type Incident,
  type State,
  type Project,
  type Pull,
  post,
  request,
  enc,
  messageOf,
} from "./api";
import type { Run } from "./actions";
import { Field, Panel, Badge, Empty, Modal, when } from "./ui";

export function Pulls({
  state,
  project,
  locked,
  canAttest,
  run,
}: {
  state: State;
  project?: Project;
  locked: boolean;
  canAttest: boolean;
  run: Run;
}) {
  const [approval, setApproval] = useState<Pull>();
  const pulls = state.pullRequests.filter(
    (p) => !project || p.projectId === project.id,
  );
  const repository = (id: string) =>
    state.projects.find((p) => p.id === id)?.repository;
  async function inspect(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget),
      id = String(form.get("project")),
      number = Number(form.get("number")),
      issueNumber = Number(form.get("issue"));
    const version =
      pulls.find((p) => p.projectId === id && p.number === number)?.version ||
      0;
    await run(
      () =>
        post("projects/" + enc(id) + "/pulls/" + number + "/refresh", {
          issueNumber,
          version,
        }),
      "Inspection refreshed from GitHub.",
    );
  }
  return (
    <>
      <Panel title="Inspect a pull request">
        <form className="panel-body" onSubmit={inspect}>
          <Field title="Repository">
            <select name="project" required defaultValue={project?.id || ""}>
              <option disabled value="">
                Choose project
              </option>
              {(project ? [project] : state.projects).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.repository}
                </option>
              ))}
            </select>
          </Field>
          <div className="form-pair">
            <Field title="Pull request number">
              <input type="number" min={1} name="number" required />
            </Field>
            <Field title="Canonical issue number">
              <input type="number" min={1} name="issue" required />
            </Field>
          </div>
          <button disabled={locked || !state.projects.length}>
            Inspect pull request
          </button>
        </form>
      </Panel>
      {pulls.length ? (
        pulls.map((p) => (
          <Panel
            key={p.projectId + p.number}
            title={"#" + p.number + " " + p.title}
            action={<Badge>{p.state}</Badge>}
          >
            <div className="panel-body">
              <a
                href={
                  "https://github.com/" +
                  repository(p.projectId) +
                  "/pull/" +
                  p.number
                }
              >
                Open on GitHub
              </a>
              <p>
                {repository(p.projectId)} · Issue #{p.issueNumber} · Inspected{" "}
                {when(p.observedAt)}
              </p>
              <dl className="detail">
                <div>
                  <dt>Target branch</dt>
                  <dd>{p.target}</dd>
                </div>
                <div>
                  <dt>Head checks</dt>
                  <dd>
                    {p.checks || "Unknown"} · {p.checkCount} checks
                  </dd>
                </div>
                <div>
                  <dt>GitHub review</dt>
                  <dd>{p.review || "Unknown"}</dd>
                </div>
                <div>
                  <dt>Head / base</dt>
                  <dd>
                    <code>
                      {p.head}
                      <br />
                      {p.base || "Unknown"}
                    </code>
                  </dd>
                </div>
                <div>
                  <dt>Combined validation</dt>
                  <dd>{p.validation || "Missing"}</dd>
                </div>
                <div>
                  <dt>Independent review</dt>
                  <dd>{p.independentReview || "Missing"}</dd>
                </div>
                <div>
                  <dt>Integration sign-off</dt>
                  <dd>
                    {p.signOff ? when(p.signOff.at) : "Awaiting sign-off"}
                  </dd>
                </div>
              </dl>
              <h3>Merge blockers</h3>
              <ul>
                {p.blockers.map((b) => (
                  <li key={b}>{b}</li>
                ))}
              </ul>
              <button
                disabled={locked}
                onClick={() =>
                  void run(
                    () =>
                      post(
                        "projects/" +
                          enc(p.projectId) +
                          "/pulls/" +
                          p.number +
                          "/refresh",
                        { issueNumber: p.issueNumber, version: p.version },
                      ),
                    "Inspection refreshed.",
                  )
                }
              >
                Refresh inspection
              </button>
              {canAttest &&
                !p.signOff &&
                p.state === "OPEN" &&
                p.issueState === "OPEN" &&
                !p.draft &&
                p.checks === "SUCCESS" &&
                p.checkCount > 0 &&
                p.review === "APPROVED" &&
                p.validation &&
                p.independentReview && (
                  <button disabled={locked} onClick={() => setApproval(p)}>
                    Review integration sign-off
                  </button>
                )}
              {p.state === "OPEN" &&
                (state.viewer?.role || "owner") === "owner" && (
                  <button
                    disabled={locked}
                    onClick={() =>
                      void run(
                        () =>
                          post(
                            "projects/" +
                              enc(p.projectId) +
                              "/pulls/" +
                              p.number +
                              "/watch",
                            { version: p.version, watching: !p.watching },
                          ),
                        p.watching
                          ? "Automatic refresh disabled."
                          : "Watching this pull request for changes.",
                      )
                    }
                  >
                    {p.watching ? "Stop watching" : "Watch checks and review"}
                  </button>
                )}
            </div>
            {(state.viewer?.role || "owner") === "owner" &&
              p.state === "OPEN" && (
                <QueueControls
                  key={p.version}
                  pull={p}
                  locked={locked}
                  run={run}
                />
              )}
            {canAttest &&
              p.state === "OPEN" &&
              p.base &&
              (!p.validation || !p.independentReview) && (
                <PullEvidence
                  key={p.version}
                  pull={p}
                  run={run}
                  locked={locked}
                />
              )}
            {p.state === "MERGED" && p.mergedSha && (
              <PullHandover
                key={p.version}
                pull={p}
                run={run}
                locked={locked}
                canAttest={canAttest}
              />
            )}
          </Panel>
        ))
      ) : (
        <Panel title="Integration queue">
          <Empty title="No inspected pull requests" icon="prs">
            Inspect a pull request to load its actual checks, review, and merge
            blockers.
          </Empty>
        </Panel>
      )}
      {approval && (
        <Modal
          title="Sign off on this integration?"
          close={() => setApproval(undefined)}
        >
          <p>
            Approve the reviewed change for {repository(approval.projectId)} #
            {approval.number} into <strong>{approval.target}</strong>.
          </p>
          <p>
            Head <code>{approval.head}</code>
          </p>
          <p>
            Target revision <code>{approval.base}</code>
          </p>
          <div className="form-footer">
            <button onClick={() => setApproval(undefined)}>Cancel</button>
            <button
              className="primary"
              disabled={
                locked ||
                !pulls.some(
                  (p) =>
                    p.projectId === approval.projectId &&
                    p.number === approval.number &&
                    p.version === approval.version,
                )
              }
              onClick={async () => {
                const { version, head, base, target } = approval;
                if (
                  await run(
                    () =>
                      post(
                        "projects/" +
                          enc(approval.projectId) +
                          "/pulls/" +
                          approval.number +
                          "/approve",
                        { version, head, base, target },
                      ),
                    "Integration sign-off recorded.",
                  )
                )
                  setApproval(undefined);
              }}
            >
              Record sign-off
            </button>
          </div>
        </Modal>
      )}
    </>
  );
}

function QueueControls({
  pull,
  locked,
  run,
}: {
  pull: Pull;
  locked: boolean;
  run: Run;
}) {
  const [environment, setEnvironment] = useState("");
  const [preview, setPreview] = useState<{ state: string; reason?: string }>();
  const recorded = pull.queueDelivery;
  const active =
    recorded &&
    [
      "requested",
      "submitting",
      "awaiting_queue",
      "queued",
      "checking",
    ].includes(recorded.state);
  const route =
    "projects/" + enc(pull.projectId) + "/pulls/" + pull.number + "/queue";
  async function action(mode: "observe" | "enforce" | "revoke" | "renew") {
    const useReceipt = mode === "revoke" || mode === "renew";
    const subject = useReceipt && recorded ? recorded : pull;
    await run(async () => {
      const result = await post<{ state: string; reason?: string }>(route, {
        mode,
        version: subject.version,
        head: subject.head,
        base: subject.base,
        target: subject.target,
        environment: useReceipt ? recorded?.environment : environment,
      });
      setPreview(result);
    });
  }
  if (!recorded && !pull.signOff) return null;
  return (
    <section className="panel-body">
      <h3>Protected queue delivery</h3>
      {recorded && (
        <p>
          {recorded.state} · {recorded.target} · {recorded.environment}.{" "}
          {recorded.reason}
        </p>
      )}
      {recorded &&
        (recorded.state === "held" || recorded.state === "revoked") &&
        recorded.version === pull.version && (
          <button disabled={locked} onClick={() => void action("renew")}>
            Start fresh integration review
          </button>
        )}
      {active ? (
        <button disabled={locked} onClick={() => void action("revoke")}>
          Revoke queue authorization
        </button>
      ) : (
        pull.signOff && (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void action("observe");
            }}
          >
            <Field title="Environment affected by this merge">
              <input
                required
                maxLength={80}
                value={environment}
                onChange={(e) => {
                  setEnvironment(e.target.value);
                  setPreview(undefined);
                }}
              />
            </Field>
            <button disabled={locked}>Preview queue policy</button>
            {preview?.reason && <p>{preview.reason}</p>}
            {preview?.state === "eligible" && (
              <button
                type="button"
                disabled={locked}
                onClick={() => void action("enforce")}
              >
                Authorize merge into {pull.target} and effects on {environment}
              </button>
            )}
          </form>
        )
      )}
    </section>
  );
}
function PullEvidence({
  pull,
  run,
  locked,
}: {
  pull: Pull;
  run: Run;
  locked: boolean;
}) {
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fields = new FormData(e.currentTarget);
    await run(
      () =>
        post(
          "projects/" +
            enc(pull.projectId) +
            "/pulls/" +
            pull.number +
            "/evidence",
          {
            version: pull.version,
            kind: fields.get("kind"),
            reference: String(fields.get("reference")).trim(),
          },
        ),
      "Evidence attached to the inspected head and target.",
    );
  }
  return (
    <form className="panel-body" onSubmit={submit}>
      <h3>Attach reviewed evidence</h3>
      <p>
        Record the SHA-256 digest of a retained artifact for the shown head and
        base. Bugsnitch rechecks the PR before accepting it.
      </p>
      <Field title="Evidence">
        <select name="kind" required>
          {!pull.validation && (
            <option value="combined_validation">Combined validation</option>
          )}
          {!pull.independentReview && (
            <option value="independent_review">Independent review</option>
          )}
        </select>
      </Field>
      <Field title="Artifact SHA-256">
        <input
          name="reference"
          pattern="[a-f0-9]{64}"
          minLength={64}
          maxLength={64}
          required
        />
      </Field>
      <button disabled={locked}>Record evidence</button>
    </form>
  );
}

function PullHandover({
  pull,
  run,
  locked,
  canAttest,
}: {
  pull: Pull;
  run: Run;
  locked: boolean;
  canAttest: boolean;
}) {
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = e.currentTarget;
    const fields = new FormData(form);
    if (
      await run(
        () =>
          post(
            "projects/" +
              enc(pull.projectId) +
              "/pulls/" +
              pull.number +
              "/handover",
            {
              version: pull.version,
              mergedSha: pull.mergedSha,
              target: pull.target,
              environment: String(fields.get("environment")).trim(),
              build: String(fields.get("build")).trim(),
              evidenceDigest: String(fields.get("digest")).trim(),
              summary: String(fields.get("summary")).trim(),
              testSteps: String(fields.get("testSteps")).trim(),
            },
          ),
        "Deployment handover recorded.",
      )
    )
      form.reset();
  }
  return (
    <div className="panel-body work-action">
      <h3>Deployment handover</h3>
      <p>
        Merged into {pull.target} at <code>{pull.mergedSha}</code>.
      </p>
      {pull.handovers?.length ? (
        pull.handovers.map((h) => (
          <details key={h.environment + "\u0000" + h.build}>
            <summary>
              {h.environment} · {h.build}
            </summary>
            <p>{h.summary}</p>
            <p>
              <strong>To test</strong> {h.testSteps}
            </p>
            <p>
              Evidence <code>{h.evidenceDigest}</code>
            </p>
            <p>
              Attested by {h.recordedBy} · {when(h.recordedAt)}
            </p>
          </details>
        ))
      ) : (
        <p>Waiting for a deployment handover.</p>
      )}
      {canAttest && (
        <form onSubmit={submit}>
          <p>
            After verifying the deployed behavior, attach the retained
            verification digest and the steps for the stakeholder to repeat.
          </p>
          <div className="form-pair">
            <Field title="Environment">
              <input name="environment" maxLength={100} required />
            </Field>
            <Field title="Immutable build">
              <input name="build" maxLength={256} required />
            </Field>
          </div>
          <Field title="Verification artifact SHA-256">
            <input
              name="digest"
              pattern="[a-f0-9]{64}"
              minLength={64}
              maxLength={64}
              required
            />
          </Field>
          <Field title="What shipped">
            <textarea name="summary" maxLength={2000} required />
          </Field>
          <Field title="What to test">
            <textarea name="testSteps" maxLength={3000} required />
          </Field>
          <button disabled={locked}>Record handover</button>
        </form>
      )}
    </div>
  );
}

interface Receipt {
  candidate: string;
  build: string;
  at: string;
  evidence: string;
}
interface Verdict {
  status: string;
  reason: string;
  environment: string;
  receiptSource: string;
  fix?: Receipt;
  deployment?: Receipt;
  validation?: Receipt;
}
export function Verification({
  incident,
  locked,
}: {
  incident: Incident;
  locked: boolean;
}) {
  const [verdict, setVerdict] = useState<Verdict>(),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(false);
  return (
    <Panel title="Environment verification">
      <form
        className="panel-body"
        onSubmit={async (e) => {
          e.preventDefault();
          const env = String(new FormData(e.currentTarget).get("env"));
          setError("");
          setVerdict(undefined);
          setLoading(true);
          try {
            setVerdict(
              await request<Verdict>(
                "projects/" +
                  enc(incident.projectId) +
                  "/incidents/" +
                  enc(incident.id) +
                  "/verification?environment=" +
                  enc(env),
              ),
            );
          } catch (err) {
            setError(messageOf(err));
          } finally {
            setLoading(false);
          }
        }}
      >
        <Field title="Deployment environment">
          <input
            name="env"
            defaultValue={incident.environment || ""}
            placeholder="production"
            required
            maxLength={80}
            onChange={() => setVerdict(undefined)}
          />
        </Field>
        <button disabled={locked || loading}>
          {loading ? "Checking…" : "Check verification"}
        </button>
        {error && (
          <p className="alert" role="alert">
            {error}
          </p>
        )}
      </form>
      {verdict && (
        <div className="panel-body">
          <Badge>{verdict.status}</Badge>
          <p>
            {verdict.environment} · {verdict.reason}
          </p>
          {(
            [
              ["Fix", verdict.fix],
              ["Deployment", verdict.deployment],
              ["Validation", verdict.validation],
            ] as const
          ).map(
            ([k, r]) =>
              r && (
                <details key={k}>
                  <summary>
                    {k} receipt · {when(r.at)}
                  </summary>
                  <p>{r.evidence}</p>
                  <p>
                    Revision <code>{r.candidate}</code>
                  </p>
                  {r.build && <p>Build {r.build}</p>}
                  <p>Recorded by {verdict.receiptSource}</p>
                </details>
              ),
          )}
        </div>
      )}
    </Panel>
  );
}
