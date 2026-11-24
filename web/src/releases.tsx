// SPDX-License-Identifier: MPL-2.0
import { useState } from "react";
import { enc, post, type State } from "./api";
import { Field, Panel, human } from "./ui";
import type { Run } from "./actions";

export function Releases({
  state,
  run,
  locked,
}: {
  state: State;
  run: Run;
  locked: boolean;
}) {
  const candidates = state.pullRequests.filter((p) => p.state === "OPEN");
  const [selected, setSelected] = useState<string[]>([]);
  const [choice, setChoice] = useState("");
  const [requestId, setRequestId] = useState(() => crypto.randomUUID());
  const key = (p: State["pullRequests"][number]) =>
    p.projectId + "/" + p.number;
  const name = (project: string) =>
    state.projects.find((p) => p.id === project)?.repository || project;
  const pulls = selected.map((k) => candidates.find((p) => key(p) === k));
  return (
    <Panel title="Multi-repository releases">
      <div className="panel-body">
        {state.releases?.map((r) => (
          <div key={r.id}>
            <h3>
              {r.name} · {r.environment}
              {r.cancelledAt ? " · Cancelled" : ""}
            </h3>
            <ol>
              {r.stages.map((s) => (
                <li key={s.projectId + "/" + s.number}>
                  {name(s.projectId)} #{s.number} · {human(s.status)}
                </li>
              ))}
            </ol>
            {!r.cancelledAt &&
              r.stages.some((s) => s.status !== "handed_over") && (
                <button
                  disabled={locked}
                  onClick={() =>
                    void run(
                      () => post("releases/" + enc(r.id) + "/cancel", {}),
                      "Release plan cancelled.",
                    )
                  }
                >
                  Cancel release plan
                </button>
              )}
          </div>
        ))}
        {new Set(candidates.map((p) => p.projectId)).size >= 2 && (
          <form
            onSubmit={async (e) => {
              e.preventDefault();
              const form = e.currentTarget;
              const data = new FormData(form);
              if (pulls.some((p) => !p)) return;
              if (
                await run(
                  () =>
                    post("releases", {
                      name: data.get("name"),
                      environment: data.get("environment"),
                      requestId,
                      stages: pulls.map((p) => ({
                        projectId: p!.projectId,
                        pullNumber: p!.number,
                        head: p!.head,
                        target: p!.target,
                      })),
                    }),
                  "Release plan created.",
                )
              ) {
                setSelected([]);
                setRequestId(crypto.randomUUID());
                form.reset();
              }
            }}
          >
            <Field title="Release name">
              <input name="name" maxLength={120} required />
            </Field>
            <Field title="Environment">
              <input name="environment" required maxLength={80} />
            </Field>
            <Field title="Next pull request in release order">
              <select
                value={choice}
                onChange={(e) => setChoice(e.target.value)}
              >
                <option value="">Choose a pull request</option>
                {candidates
                  .filter((p) => !selected.includes(key(p)))
                  .map((p) => (
                    <option key={key(p)} value={key(p)}>
                      {name(p.projectId)} #{p.number}
                    </option>
                  ))}
              </select>
            </Field>
            <button
              type="button"
              disabled={!choice || locked || selected.length >= 16}
              onClick={() => {
                setSelected([...selected, choice]);
                setChoice("");
              }}
            >
              Add stage
            </button>
            <ol>
              {selected.map((k, i) => (
                <li key={k}>
                  {pulls[i]
                    ? name(pulls[i]!.projectId) + " #" + pulls[i]!.number
                    : "Unavailable PR"}{" "}
                  <button
                    type="button"
                    disabled={locked}
                    onClick={() => setSelected(selected.filter((x) => x !== k))}
                  >
                    Remove
                  </button>
                </li>
              ))}
            </ol>
            <button
              disabled={
                locked ||
                pulls.some((p) => !p) ||
                new Set(pulls.map((p) => p?.projectId)).size < 2
              }
            >
              Create release plan
            </button>
          </form>
        )}
        {new Set(candidates.map((p) => p.projectId)).size < 2 && (
          <p>
            Inspect open pull requests from at least two projects to plan a
            release.
          </p>
        )}
      </div>
    </Panel>
  );
}
