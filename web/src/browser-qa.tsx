// SPDX-License-Identifier: MPL-2.0
import { useRef, useState } from "react";
import { enc, messageOf, post, request, type Incident } from "./api";
import type { Run } from "./actions";
import { Badge, Field, Panel, when } from "./ui";

interface Observation {
  revision: string;
  result: "pass" | "fail";
  observed: string;
  screenshot: string;
}
interface Report {
  protocol: number;
  attemptId: string;
  issueNumber: number;
  assertion: string;
  checkedAt: string;
  before: Observation;
  after: Observation;
}

function isObservation(
  value: Partial<Observation> | undefined,
): value is Observation {
  return (
    !!value &&
    /^[0-9a-f]{40}([0-9a-f]{24})?$/.test(value.revision || "") &&
    (value.result === "pass" || value.result === "fail") &&
    typeof value.observed === "string" &&
    !!value.observed.trim() &&
    value.observed.length <= 512 &&
    typeof value.screenshot === "string" &&
    value.screenshot.length <= 109228 &&
    value.screenshot.startsWith("iVBORw0KGgo") &&
    /^[A-Za-z0-9+/]+=*$/.test(value.screenshot)
  );
}

function ReportDetails({ report }: { report: Report }) {
  return (
    <>
      <p>
        <strong>{report.assertion}</strong>
      </p>
      <p className="subtle">Checked {when(report.checkedAt)}</p>
      <div className="form-pair qa-comparison">
        {(
          [
            ["Before", report.before],
            ["After", report.after],
          ] as const
        ).map(([label, observation]) => (
          <figure key={label}>
            <figcaption>
              <strong>{label}</strong>{" "}
              <Badge tone={observation.result === "pass" ? "neutral" : "warn"}>
                {observation.result === "pass" ? "Pass" : "Fail"}
              </Badge>
              <p>{observation.observed}</p>
              <code title={observation.revision}>
                {observation.revision.slice(0, 12)}
              </code>
            </figcaption>
            <img
              src={`data:image/png;base64,${observation.screenshot}`}
              alt={`${label} browser screenshot. ${observation.observed}`}
            />
          </figure>
        ))}
      </div>
    </>
  );
}

interface Share {
  incident: Incident;
  run: Run;
}

export function IncidentBrowserQA({
  incident,
  locked,
  canShare,
  run,
}: {
  incident: Incident;
  locked: boolean;
  canShare: boolean;
  run: Run;
}) {
  return (
    <Panel title="Browser QA evidence">
      <BrowserQA
        path={`projects/${enc(incident.projectId)}/incidents/${enc(incident.id)}/browser-qa`}
        locked={locked}
        share={canShare && incident.issueNumber ? { incident, run } : undefined}
      />
    </Panel>
  );
}

export function BrowserQA({
  path,
  locked,
  share,
  candidate,
}: {
  path: string;
  locked: boolean;
  share?: Share;
  candidate?: string;
}) {
  const [report, setReport] = useState<Report | null>();
  const [draft, setDraft] = useState<Report>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const selected = useRef(0);

  async function load() {
    setError("");
    setLoading(true);
    try {
      const response = await request<{ report: Report | null }>(path);
      setReport(
        !candidate || response.report?.after.revision === candidate
          ? response.report
          : null,
      );
    } catch (error) {
      setError(messageOf(error));
    } finally {
      setLoading(false);
    }
  }

  async function preview(file: File | undefined) {
    const selection = ++selected.current;
    setDraft(undefined);
    setError("");
    if (!file || !share) return;
    try {
      if (file.size > 224 * 1024)
        throw new Error("Choose a browser QA report under 224 KiB.");
      const value = JSON.parse(await file.text()) as Partial<Report> | null;
      if (
        !value ||
        value.protocol !== 1 ||
        value.issueNumber !== share.incident.issueNumber ||
        !/^[0-9a-f]{32}$/.test(value.attemptId || "") ||
        typeof value.assertion !== "string" ||
        !value.assertion.trim() ||
        value.assertion.length > 512 ||
        typeof value.checkedAt !== "string" ||
        !Number.isFinite(Date.parse(value.checkedAt)) ||
        !isObservation(value.before) ||
        !isObservation(value.after) ||
        value.before.revision === value.after.revision
      ) {
        throw new Error(
          "Choose a valid browser QA report for this incident’s linked GitHub issue.",
        );
      }
      if (selected.current === selection) setDraft(value as Report);
    } catch (error) {
      if (selected.current === selection) setError(messageOf(error));
    }
  }

  async function publish() {
    if (!draft || !share) return;
    const ok = await share.run(
      () => post(path, { version: share.incident.version, report: draft }),
      "Browser QA evidence shared.",
    );
    if (ok) {
      setReport(draft);
      setDraft(undefined);
    }
  }
  return (
    <div className="panel-body">
      <button
        type="button"
        disabled={locked || loading}
        onClick={() => void load()}
      >
        {loading
          ? "Loading browser QA…"
          : report === undefined
            ? "View browser QA evidence"
            : "Refresh browser QA evidence"}
      </button>
      {error && <p role="alert">{error}</p>}
      {report === null && (
        <p role="status">
          No browser QA evidence has been shared for{" "}
          {candidate ? "this pull request head" : "this incident"}.
        </p>
      )}
      {report && <ReportDetails report={report} />}
      {share && (
        <details>
          <summary>Share a browser QA report</summary>
          <p>
            Capture a report on the original runner with{" "}
            <code>bugsnitch local-browser-qa --id ATTEMPT_ID</code>. Preview
            both screenshots before sharing them with this project.
          </p>
          <Field title="Browser QA report file">
            <input
              type="file"
              accept=".json,application/json"
              disabled={locked}
              onChange={(e) => {
                void preview(e.currentTarget.files?.[0]);
                e.currentTarget.value = "";
              }}
            />
          </Field>
          {draft && (
            <>
              <ReportDetails report={draft} />
              <button
                type="button"
                disabled={locked}
                onClick={() => void publish()}
              >
                Share QA report with project
              </button>
            </>
          )}
        </details>
      )}
    </div>
  );
}
