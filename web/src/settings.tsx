// SPDX-License-Identifier: MPL-2.0
import { useState, type FormEvent } from "react";
import { enc, post, type Project, type State, type Policy } from "./api";
import { Panel, Field, Badge, Modal } from "./ui";
import type { Run } from "./actions";

const policyLabels: Record<keyof Policy, string> = {
  requirePlan: "Require an approved plan for execution",
  allowPublication: "Publish approved plans as GitHub issues",
  allowPromotion: "Promote incidents to GitHub issues",
  allowDraftPR: "Open draft pull requests",
  autoTriage: "Automatically promote qualifying Sentry incidents",
};
const defaults: Record<string, Policy> = {
  Assisted: {
    requirePlan: true,
    allowPublication: false,
    allowPromotion: false,
    allowDraftPR: false,
    autoTriage: false,
  },
  Supervised: {
    requirePlan: false,
    allowPublication: true,
    allowPromotion: true,
    allowDraftPR: true,
    autoTriage: false,
  },
  "Autonomous within limits": {
    requirePlan: true,
    allowPublication: true,
    allowPromotion: true,
    allowDraftPR: true,
    autoTriage: true,
  },
};
const policyKeys = Object.keys(policyLabels) as (keyof Policy)[];
function PolicyEditor({
  project,
  run,
  locked,
}: {
  project: Project;
  run: Run;
  locked: boolean;
}) {
  const [editing, setEditing] = useState(false),
    [confirm, setConfirm] = useState(false);
  const [preset, setPreset] = useState(project.preset || "Supervised");
  const [rules, setRules] = useState<Policy>(
    () =>
      Object.fromEntries(
        policyKeys.map((k) => [k, !!project[k]]),
      ) as unknown as Policy,
  );
  const changed =
    policyKeys.some((k) => rules[k] !== project[k]) ||
    preset !== project.preset;
  const custom =
    !defaults[preset] ||
    policyKeys.some((k) => rules[k] !== defaults[preset][k]);
  const invalid = rules.autoTriage && !rules.allowPromotion;
  const labels = (values: Policy) => (
    <dl className="detail">
      {policyKeys.map((k) => (
        <div key={k}>
          <dt>{policyLabels[k]}</dt>
          <dd>{values[k] ? "Enabled" : "Disabled"}</dd>
        </div>
      ))}
    </dl>
  );
  return (
    <Panel
      title={project.name + " autonomy"}
      action={<Badge>{project.preset || "Settings unavailable"}</Badge>}
    >
      <div className="panel-body">
        {labels(project)}
        <button
          disabled={locked || !project.preset}
          onClick={() => {
            setEditing(true);
            setConfirm(false);
          }}
        >
          Edit project policy
        </button>
      </div>
      {editing && (
        <Modal
          title={confirm ? "Confirm policy change" : "Edit project policy"}
          close={() => {
            if (!locked) setEditing(false);
          }}
        >
          {confirm ? (
            <>
              <p>
                Apply these permissions to {project.repository} at project
                version {project.version}. Changes take effect when the server
                accepts them.
              </p>
              {labels(rules)}
              {rules.autoTriage && (
                <p className="hint">
                  Three distinct Sentry reports in 24 hours for a known matching
                  build and environment can create a GitHub issue automatically.
                </p>
              )}
              <div className="form-footer">
                <button onClick={() => setConfirm(false)}>Back</button>
                <button
                  className="primary"
                  disabled={locked}
                  onClick={async () => {
                    const ok = await run(
                      () =>
                        post("projects/" + enc(project.id) + "/policy", {
                          version: project.version,
                          preset: defaults[preset] ? preset : "Supervised",
                          ...(custom ? { overrides: rules } : {}),
                        }),
                      "Project policy saved.",
                    );
                    if (ok) setEditing(false);
                  }}
                >
                  Apply policy
                </button>
              </div>
            </>
          ) : (
            <>
              <Field title="Autonomy preset">
                <select
                  value={preset}
                  onChange={(e) => {
                    setPreset(e.target.value);
                    setRules({ ...defaults[e.target.value] });
                  }}
                >
                  {!defaults[preset] && (
                    <option value={preset}>{preset}</option>
                  )}
                  {Object.keys(defaults).map((p) => (
                    <option key={p}>{p}</option>
                  ))}
                </select>
              </Field>
              {policyKeys.map((k) => (
                <label className="checkrow" key={k}>
                  <input
                    type="checkbox"
                    checked={rules[k]}
                    onChange={(e) =>
                      setRules({ ...rules, [k]: e.target.checked })
                    }
                  />
                  {policyLabels[k]}
                </label>
              ))}
              {invalid && (
                <p role="status">
                  Automatic triage requires incident promotion.
                </p>
              )}
              <div className="form-footer">
                <button onClick={() => setEditing(false)}>Cancel</button>
                <button
                  className="primary"
                  disabled={!changed || invalid || locked}
                  onClick={() => setConfirm(true)}
                >
                  Review changes
                </button>
              </div>
            </>
          )}
        </Modal>
      )}
    </Panel>
  );
}
function Retention({
  project,
  run,
  locked,
}: {
  project: Project;
  run: Run;
  locked: boolean;
}) {
  const [days, setDays] = useState(project.retentionDays);
  const [confirm, setConfirm] = useState(false);
  async function save() {
    if (
      await run(
        () =>
          post("projects/" + enc(project.id) + "/retention", {
            version: project.version,
            days,
          }),
        "Retention period saved.",
      )
    )
      setConfirm(false);
  }
  return (
    <Panel title={project.name + " retention"}>
      <form
        className="panel-body"
        onSubmit={(e) => {
          e.preventDefault();
          if (days < project.retentionDays) setConfirm(true);
          else void save();
        }}
      >
        <Field title="Content retention in days">
          <input
            type="number"
            min={1}
            max={3650}
            required
            value={days || ""}
            onChange={(e) => setDays(Number(e.target.value))}
          />
        </Field>
        <p>
          Applies to shared diffs, chat message bodies, and sanitized incident
          observations. The service preserves record identities and audit
          events.
        </p>
        <button disabled={locked || days === project.retentionDays}>
          Save retention
        </button>
      </form>
      {confirm && (
        <Modal
          title="Shorten content retention?"
          close={() => setConfirm(false)}
        >
          <p>
            The next cleanup can expire existing content older than {days} days
            in {project.name}.
          </p>
          <div className="form-footer">
            <button onClick={() => setConfirm(false)}>Cancel</button>
            <button
              className="primary"
              disabled={locked}
              onClick={() => void save()}
            >
              Apply shorter retention
            </button>
          </div>
        </Modal>
      )}
    </Panel>
  );
}

export function Settings({
  projects,
  state,
  locked,
  run,
}: {
  projects: Project[];
  state: State;
  locked: boolean;
  run: Run;
}) {
  const limits = state.limits;
  async function save(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!limits) return;
    const f = new FormData(e.currentTarget);
    await run(
      () =>
        post("limits", {
          tickets: Number(f.get("tickets")),
          turns: Number(f.get("turns")),
          version: limits.version,
        }),
      "Workspace limits updated.",
    );
  }
  return (
    <>
      {projects.map((p) => (
        <PolicyEditor
          key={p.id + ":" + p.version}
          project={p}
          run={run}
          locked={locked}
        />
      ))}
      {projects
        .filter((p) => p.retentionDays > 0)
        .map((p) => (
          <Retention
            key={p.id + ":" + p.version}
            project={p}
            run={run}
            locked={locked}
          />
        ))}
      {limits && (
        <Panel title="Workspace capacity">
          <form key={limits.version} className="panel-body" onSubmit={save}>
            <div className="form-pair">
              <Field title="Active implementation tickets">
                <input
                  required
                  type="number"
                  min={1}
                  max={32}
                  name="tickets"
                  defaultValue={limits.tickets}
                />
              </Field>
              <Field title="Simultaneous model turns">
                <input
                  required
                  type="number"
                  min={1}
                  max={32}
                  name="turns"
                  defaultValue={limits.turns}
                />
              </Field>
            </div>
            <button disabled={locked}>Save limits</button>
          </form>
          {state.devices
            .filter(
              (device) => !device.revoked && device.turnLimit !== undefined,
            )
            .map((device) => (
              <form
                key={device.id + ":" + limits.version}
                className="panel-body"
                onSubmit={(e) => {
                  e.preventDefault();
                  const turns = Number(
                    new FormData(e.currentTarget).get("turns"),
                  );
                  void run(
                    () =>
                      post("devices/" + enc(device.id) + "/limits", {
                        turns,
                        version: limits.version,
                      }),
                    "Device limit updated.",
                  );
                }}
              >
                <Field title={device.name + " simultaneous model turns"}>
                  <input
                    required
                    type="number"
                    min={1}
                    max={32}
                    name="turns"
                    defaultValue={device.turnLimit}
                  />
                </Field>
                <button disabled={locked}>Save device limit</button>
              </form>
            ))}
        </Panel>
      )}
    </>
  );
}
