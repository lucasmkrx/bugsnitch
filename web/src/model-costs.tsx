// SPDX-License-Identifier: MPL-2.0
import { useState, type FormEvent } from "react";
import { enc, messageOf, post, type Project } from "./api";
import { Panel, Field } from "./ui";

interface Price {
  version: number;
  inputPerMillionUSD: number | null;
  outputPerMillionUSD: number | null;
}
interface Threshold {
  projectId: string;
  version: number;
  mode: "off" | "alert" | "stop";
  thresholdUSD: number | null;
  month: string;
  estimatedUSD: number;
  admittedTurns: number;
  meteredTurns: number;
  pricedTurns: number;
  reached: boolean;
}
interface Usage {
  monthlyThreshold?: Threshold;
  workspacePrices?: Record<string, Price> | null;
  admittedTurns: number;
  unmeteredTurns: number;
  unpricedMeteredTurns: number;
  turnsOnFailedOrCancelledAttempts: number;
  models: {
    model: string;
    turns: number;
    pricedTurns: number;
    inputTokens: number;
    outputTokens: number;
    estimatedUSD?: number;
  }[];
}
export function ModelCosts({
  projects,
  scope,
}: {
  projects: Project[];
  scope?: string | null;
}) {
  const [report, setReport] = useState<Usage>(),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget),
      project = String(form.get("project") || scope || "");
    setBusy(true);
    setError("");
    setReport(undefined);
    try {
      const prices = String(form.get("rates") || "").trim();
      setReport(
        await post<Usage>(
          project ? "projects/" + enc(project) + "/usage" : "usage",
          {
            days: Number(form.get("days")),
            rates: prices ? JSON.parse(prices) : {},
          },
        ),
      );
    } catch (err) {
      setError(messageOf(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Panel title="Model usage and cost">
      <form
        className="panel-body"
        onSubmit={submit}
        onChange={() => setReport(undefined)}
      >
        <div className="form-pair">
          <Field title="Usage scope">
            <select name="project" defaultValue={scope || ""}>
              {!scope && <option value="">Workspace</option>}
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.repository}
                </option>
              ))}
            </select>
          </Field>
          <Field title="Days of usage">
            <input
              type="number"
              name="days"
              min="1"
              max="365"
              defaultValue={30}
              required
            />
          </Field>
        </div>
        <details>
          <summary>Estimate with different prices</summary>
          <Field title="USD per million input and output tokens">
            <textarea
              name="rates"
              placeholder={
                '{"model-id":{"inputPerMillionUSD":1,"outputPerMillionUSD":5}}'
              }
              maxLength={16384}
            />
          </Field>
        </details>
        <button disabled={busy}>
          {busy ? "Reading usage…" : "Read usage"}
        </button>
        {error && (
          <p className="alert" role="alert">
            {error}
          </p>
        )}
      </form>
      {report && (
        <div className="panel-body">
          <p>
            Estimated cost for{" "}
            {report.admittedTurns -
              report.unmeteredTurns -
              report.unpricedMeteredTurns}{" "}
            priced turns ·{" "}
            {report.models.some((m) => m.pricedTurns > 0)
              ? "$" +
                report.models
                  .reduce((sum, m) => sum + (m.estimatedUSD ?? 0), 0)
                  .toLocaleString(undefined, { maximumFractionDigits: 6 })
              : "Unknown"}
          </p>
          <p>
            {report.admittedTurns - report.unmeteredTurns} of{" "}
            {report.admittedTurns} admitted turns have measured tokens.{" "}
            {report.unmeteredTurns} unmetered.
          </p>
          <p>
            {report.turnsOnFailedOrCancelledAttempts} turns belonged to failed
            or cancelled attempts. {report.unpricedMeteredTurns} metered turns
            have no supplied price.
          </p>
          {report.models.map((m) => (
            <div key={m.model}>
              <strong>{m.model}</strong>
              <p>
                {m.turns} turns · {m.inputTokens.toLocaleString()} input tokens
                · {m.outputTokens.toLocaleString()} output tokens
                {m.estimatedUSD !== undefined
                  ? " · estimated $" +
                    m.estimatedUSD.toLocaleString(undefined, {
                      maximumFractionDigits: 6,
                    })
                  : ""}
              </p>
            </div>
          ))}
        </div>
      )}
      {report?.monthlyThreshold && (
        <ThresholdEditor
          key={
            report.monthlyThreshold.projectId +
            "/" +
            report.monthlyThreshold.version
          }
          policy={report.monthlyThreshold}
          saved={(next) =>
            setReport((current) =>
              current?.monthlyThreshold?.projectId === next.projectId
                ? { ...current, monthlyThreshold: next }
                : current,
            )
          }
        />
      )}
      {report?.workspacePrices && (
        <RateEditor
          prices={report.workspacePrices}
          onChange={() => setReport(undefined)}
        />
      )}
    </Panel>
  );
}

function ThresholdEditor({
  policy,
  saved,
}: {
  policy: Threshold;
  saved: (next: Threshold) => void;
}) {
  const [mode, setMode] = useState(policy.mode),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const scope = policy.projectId ? "project" : "workspace";
  async function save(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const raw = new FormData(e.currentTarget).get("threshold");
    const path = policy.projectId
      ? "projects/" + enc(policy.projectId) + "/usage/threshold"
      : "usage/threshold";
    setBusy(true);
    setError("");
    try {
      saved(
        await post<Threshold>(path, {
          mode,
          thresholdUSD: mode === "off" ? null : Number(raw),
          version: policy.version,
        }),
      );
    } catch (err) {
      setError(messageOf(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <form className="panel-body" onSubmit={save}>
      <h3>Monthly {scope} cost threshold</h3>
      <p>
        {policy.month.slice(0, 7)} UTC · $
        {policy.estimatedUSD.toLocaleString(undefined, {
          maximumFractionDigits: 6,
        })}{" "}
        estimated from {policy.pricedTurns} priced turns.{" "}
        {policy.admittedTurns - policy.meteredTurns} have no measurement.{" "}
        {policy.meteredTurns - policy.pricedTurns} have no effective price.
      </p>
      {policy.reached && (
        <p className="alert" role="status">
          {policy.mode === "stop"
            ? "New paired turns are paused for this scope."
            : "The estimated cost reached your alert threshold."}
        </p>
      )}
      <div className="form-pair">
        <Field title="When the threshold is reached">
          <select
            value={mode}
            onChange={(e) => setMode(e.target.value as Threshold["mode"])}
          >
            <option value="off">Off</option>
            <option value="alert">Alert in activity</option>
            <option value="stop">Alert and stop new turns</option>
          </select>
        </Field>
        {mode !== "off" && (
          <Field title="Estimated USD per UTC month">
            <input
              name="threshold"
              type="number"
              required
              min="0.000001"
              max="1000000000"
              step="0.000001"
              defaultValue={policy.thresholdUSD ?? ""}
            />
          </Field>
        )}
      </div>
      <p>
        Only measured tokens with a saved model price count toward this
        threshold. In-flight turns can finish and post usage later. Actual
        invoices may exceed the estimate.
      </p>
      {error && (
        <p className="alert" role="alert">
          {error}
        </p>
      )}
      <button disabled={busy}>
        {busy ? "Saving…" : "Save cost threshold"}
      </button>
    </form>
  );
}

function RateEditor({
  prices,
  onChange,
}: {
  prices: Record<string, Price>;
  onChange: () => void;
}) {
  const [model, setModel] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const selected = prices[model];
  async function save(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    const remove =
      (e.nativeEvent as SubmitEvent).submitter?.getAttribute("value") ===
      "remove";
    setError("");
    setBusy(true);
    try {
      await post("usage/prices", {
        model,
        version: selected?.version ?? 0,
        ...(remove
          ? { remove: true }
          : {
              inputPerMillionUSD: Number(form.get("input")),
              outputPerMillionUSD: Number(form.get("output")),
            }),
      });
      onChange();
    } catch (err) {
      setError(messageOf(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <details className="panel-body">
      <summary>Set workspace model prices</summary>
      <form onSubmit={save}>
        <p>
          New prices apply to turns admitted after saving. Enter USD per million
          tokens.
        </p>
        <Field title="Exact model ID">
          <input
            name="model"
            list="priced-models"
            required
            maxLength={120}
            value={model}
            onChange={(e) => setModel(e.target.value)}
          />
        </Field>
        <datalist id="priced-models">
          {Object.keys(prices).map((id) => (
            <option value={id} key={id} />
          ))}
        </datalist>
        <div className="form-pair" key={model}>
          <Field title="Input price">
            <input
              type="number"
              min="0"
              max="100000"
              step="0.00000001"
              name="input"
              required
              defaultValue={selected?.inputPerMillionUSD ?? ""}
            />
          </Field>
          <Field title="Output price">
            <input
              type="number"
              min="0"
              max="100000"
              step="0.00000001"
              name="output"
              required
              defaultValue={selected?.outputPerMillionUSD ?? ""}
            />
          </Field>
        </div>
        <button disabled={busy}>Save future price</button>
        {selected?.inputPerMillionUSD != null && (
          <button disabled={busy} formNoValidate value="remove">
            End price
          </button>
        )}
        {error && (
          <p className="alert" role="alert">
            {error}
          </p>
        )}
      </form>
    </details>
  );
}
