// SPDX-License-Identifier: MPL-2.0
import { useState, type FormEvent } from "react";
import { post, messageOf, type Pull } from "./api";
import { Field, when } from "./ui";

export interface MonitorState {
  monitor?: { id: string; check: string; candidate: string };
  holds?: { id: number; reason: string; evidence: string; at: string }[];
}
export interface GateCredential {
  monitorId: string;
  gateToken: string;
}

export function DeploymentMonitor({
  path,
  pulls,
  version,
  environment,
  candidate,
  state,
  locked,
  onCredential,
  onChanged,
}: {
  path: string;
  pulls: Pull[];
  version: number;
  environment: string;
  candidate?: string;
  state: MonitorState;
  locked: boolean;
  onCredential: (value: GateCredential) => void;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [selected, setSelected] = useState(pulls[0]?.number ?? 0);
  const merged = pulls.find((p) => p.number === selected);
  async function change(operation: () => Promise<void>) {
    setBusy(true);
    setError("");
    try {
      await operation();
      onChanged();
    } catch (err) {
      setError(messageOf(err));
    } finally {
      setBusy(false);
    }
  }
  function configure(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    void change(async () => {
      const credential = await post<GateCredential>(
        path + "/deployment-monitor",
        {
          version,
          environment,
          candidate: merged?.mergedSha || candidate,
          ...(merged
            ? {
                pullNumber: merged.number,
                pullVersion: merged.version,
                fixCheck: f.get("fixCheck"),
                fixVerifierAppId: Number(f.get("fixApp")),
              }
            : {}),
          check: f.get("check"),
          deployerId: Number(f.get("deployer")),
          verifierAppId: Number(f.get("verifier")),
          windowSeconds: Number(f.get("window")),
          minimumExposures: Number(f.get("minimum")),
        },
      );
      onCredential(credential);
    });
  }
  return (
    <div>
      {state.holds?.map((h) => (
        <div key={h.id}>
          <p>
            <strong>Promotion paused</strong> · {h.reason} · {when(h.at)}
          </p>
          <p>
            Evidence <code>{h.evidence}</code>
          </p>
          <button
            disabled={locked || busy}
            onClick={() =>
              void change(async () => {
                await post(path + "/deployment-holds/clear", { holdId: h.id });
              })
            }
          >
            Acknowledge hold {h.id}
          </button>
        </div>
      ))}
      {state.monitor ? (
        <details>
          <summary>Deployment monitor · {state.monitor.check}</summary>
          <p>
            Monitor <code>{state.monitor.id}</code>
          </p>
          <button
            disabled={locked || busy}
            onClick={() =>
              void change(async () => {
                await post(path + "/deployment-monitor/disable", {
                  monitorId: state.monitor!.id,
                });
              })
            }
          >
            Disable monitor and revoke gate token
          </button>
        </details>
      ) : (
        (candidate || pulls.length > 0) &&
        !locked && (
          <details>
            <summary>Connect a GitHub deployment monitor</summary>
            <form onSubmit={configure}>
              <p>
                Use the trusted deployment publisher and the separate GitHub App
                that checks the affected behavior in {environment}.
              </p>
              {pulls.length > 0 && (
                <Field title="Fix revision">
                  <select
                    value={selected}
                    onChange={(e) => setSelected(Number(e.target.value))}
                  >
                    {pulls.map((p) => (
                      <option key={p.number} value={p.number}>
                        Merged PR #{p.number} · {p.mergedSha?.slice(0, 12)}
                      </option>
                    ))}
                    {candidate && (
                      <option value={0}>
                        Validated fix · {candidate.slice(0, 12)}
                      </option>
                    )}
                  </select>
                </Field>
              )}
              {merged && (
                <div className="form-pair">
                  <Field title="Integrated regression check name">
                    <input name="fixCheck" maxLength={100} required />
                  </Field>
                  <Field title="Integrated regression GitHub App ID">
                    <input name="fixApp" type="number" min="1" required />
                  </Field>
                </div>
              )}
              <Field title="Behavior check name">
                <input name="check" maxLength={100} required />
              </Field>
              <div className="form-pair">
                <Field title="Deployment publisher user ID">
                  <input type="number" name="deployer" min="1" required />
                </Field>
                <Field title="Verifier GitHub App ID">
                  <input type="number" name="verifier" min="1" required />
                </Field>
              </div>
              <div className="form-pair">
                <Field title="Observation window (seconds)">
                  <input
                    type="number"
                    name="window"
                    min="0"
                    max="7776000"
                    defaultValue={0}
                    required
                  />
                </Field>
                <Field title="Minimum observed exposures">
                  <input
                    type="number"
                    name="minimum"
                    min="0"
                    max="1000000000"
                    defaultValue={0}
                    required
                  />
                </Field>
              </div>
              <button disabled={busy}>Connect deployment monitor</button>
            </form>
          </details>
        )
      )}
      {error && (
        <p role="alert" className="alert">
          {error}
        </p>
      )}
    </div>
  );
}
