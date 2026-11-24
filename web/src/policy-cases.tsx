// SPDX-License-Identifier: MPL-2.0
import { useEffect, useState } from "react";
import { enc, messageOf, post, request, type Project } from "./api";
import { Panel, Field } from "./ui";
import type { Run } from "./actions";

type Subject = {
  number: number;
  head: string;
  base: string;
  target: string;
  label: string;
};
export function PolicyCases({
  project,
  run,
  locked,
}: {
  project: Project;
  run: Run;
  locked: boolean;
}) {
  const [open, setOpen] = useState(false),
    [revision, setRevision] = useState(0);
  const [rows, setRows] = useState<Subject[]>(),
    [error, setError] = useState("");
  const path = "projects/" + enc(project.id);
  useEffect(() => {
    if (!open) return;
    const a = new AbortController();
    void request<Subject[]>(path + "/review-subjects", { signal: a.signal })
      .then((r) => {
        setRows(r);
        setError("");
      })
      .catch((e: unknown) => {
        if (!a.signal.aborted) setError(messageOf(e));
      });
    return () => a.abort();
  }, [path, open, revision]);
  return (
    <Panel title={project.name + " approval policy history"}>
      <div className="panel-body">
        <button onClick={() => setOpen(!open)} aria-expanded={open}>
          {open ? "Close history" : "Review historical cases"}
        </button>
        {open && (
          <>
            <p>
              <a
                href={"/api/v1/" + path + "/policy-cases"}
                download={
                  project.repository.replace("/", "-") + "-policy-cases.json"
                }
              >
                Download labelled cases for policy replay
              </a>
            </p>
            {error && <p role="alert">{error}</p>}
            {rows?.length === 0 && (
              <p>
                Inspect an open pull request to capture its head and target
                revision.
              </p>
            )}
            {rows?.some((r) => !r.label) && (
              <form
                onSubmit={async (e) => {
                  e.preventDefault();
                  const form = e.currentTarget;
                  const f = new FormData(form);
                  const file = f.get("evidence");
                  let evidence: unknown;
                  if (file instanceof File && file.size) {
                    try {
                      if (file.size > 16384) throw new Error();
                      evidence = JSON.parse(await file.text());
                    } catch {
                      setError(
                        "Select a review evidence JSON file under 16 KiB.",
                      );
                      return;
                    }
                  }
                  const r = rows[Number(f.get("subject"))];
                  if (!r) return;
                  if (
                    await run(
                      () =>
                        post(path + "/pulls/" + r.number + "/policy-case", {
                          head: r.head,
                          base: r.base,
                          target: r.target,
                          label: f.get("label"),
                          cohort: f.get("cohort"),
                          reference: f.get("reference"),
                          evidence,
                        }),
                      "Historical outcome recorded.",
                    )
                  ) {
                    form.reset();
                    setRevision((x) => x + 1);
                  }
                }}
              >
                <Field title="Observed pull request">
                  <select name="subject">
                    {rows.map(
                      (r, i) =>
                        !r.label && (
                          <option key={i} value={i}>
                            #{r.number} · {r.head.slice(0, 12)} into {r.target}{" "}
                            at {r.base.slice(0, 12)}
                          </option>
                        ),
                    )}
                  </select>
                </Field>
                <Field title="Outcome">
                  <select name="label">
                    <option value="regression">Confirmed regression</option>
                    <option value="revert">Reverted</option>
                    <option value="clean">Confirmed clean control</option>
                  </select>
                </Field>
                <Field title="Evaluation set">
                  <select name="cohort">
                    <option value="holdout">Holdout</option>
                    <option value="calibration">Calibration</option>
                  </select>
                </Field>
                <Field title="Evidence SHA-256">
                  <input name="reference" pattern="[0-9a-f]{64}" required />
                </Field>
                <Field title="Historical review and checks (optional JSON)">
                  <input
                    name="evidence"
                    type="file"
                    accept="application/json,.json"
                  />
                </Field>
                <button disabled={locked}>Record outcome</button>
              </form>
            )}
          </>
        )}
      </div>
    </Panel>
  );
}
