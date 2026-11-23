// SPDX-License-Identifier: MPL-2.0
import { useState } from "react";
import { post, enc, type State, type Project, type Publication } from "./api";
import { Panel, Badge, human, Modal, link } from "./ui";
import type { Run } from "./actions";

export function Publications({
  state,
  project,
  run,
  locked,
}: {
  state: State;
  project?: Project;
  run: Run;
  locked: boolean;
}) {
  const [selected, setSelected] = useState<Publication | null>(null);
  const list = (state.publications || []).filter(
    (p) => !project || project.id === p.projectId,
  );
  const repository = (id: string) =>
    state.projects.find((p) => p.id === id)?.repository;
  return (
    <>
      {list.map((p) => (
        <Panel
          title={p.document.title}
          key={p.id}
          action={<Badge>{human(p.status)}</Badge>}
        >
          <div className="panel-body">
            <h3>GitHub issue publication</h3>
            <p>
              {repository(p.projectId)} · {p.document.tickets?.length ?? 0}{" "}
              proposed tickets
            </p>
            {p.document.tickets?.map((t) => {
              const issue = p.issues.find((i) => i.localNumber === t.number);
              return (
                <details key={t.number}>
                  <summary>
                    {t.title} ·{" "}
                    {issue?.number
                      ? "#" + issue.number
                      : human(issue?.status || "proposed")}
                  </summary>
                  <p>{t.criteria?.join(" · ") || "No criteria recorded"}</p>
                  <p>{t.verification}</p>
                  {issue?.reason && <p>{issue.reason}</p>}
                  {issue?.number && (
                    <a
                      href={
                        "https://github.com/" +
                        repository(p.projectId) +
                        "/issues/" +
                        issue.number
                      }
                    >
                      Open GitHub issue #{issue.number}
                    </a>
                  )}
                </details>
              );
            })}
            {p.planId && (
              <a href={link("plans", p.projectId, p.planId)}>
                Inspect the plan with canonical issue numbers
              </a>
            )}
            {p.status === "proposed" && (
              <button disabled={locked} onClick={() => setSelected(p)}>
                Review publication
              </button>
            )}
          </div>
        </Panel>
      ))}
      {selected && (
        <Modal
          title="Create GitHub issues?"
          close={() => {
            if (!locked) setSelected(null);
          }}
        >
          <p>
            This authorizes Bugsnitch to create{" "}
            {selected.document.tickets?.length ?? 0} issues in{" "}
            {repository(selected.projectId)}. Review the generated plan
            separately before approving execution.
          </p>
          <ul>
            {selected.document.tickets?.map((t) => (
              <li key={t.number}>{t.title}</li>
            ))}
          </ul>
          <div className="form-footer">
            <button disabled={locked} onClick={() => setSelected(null)}>
              Cancel
            </button>
            <button
              className="primary"
              disabled={locked}
              onClick={async () => {
                const ok = await run(
                  () =>
                    post("publications/" + enc(selected.id) + "/approve", {
                      version: selected.version,
                    }),
                  "Publication accepted. Canonical numbers appear as GitHub confirms each issue.",
                );
                if (ok) setSelected(null);
              }}
            >
              Authorize issue creation
            </button>
          </div>
        </Modal>
      )}
    </>
  );
}
