// SPDX-License-Identifier: MPL-2.0
import { useEffect, useState, type FormEvent } from "react";
import { enc, messageOf, post, request, type Project } from "./api";
import type { Run } from "./actions";
import { Field, Modal, Panel, human } from "./ui";

interface Member {
  id: string;
  name: string;
  role: "operator" | "approver";
  projectId?: string | null;
  revokedAt: string | null;
}

export function Members({
  locked,
  run,
  projects,
}: {
  locked: boolean;
  run: Run;
  projects: Project[];
}) {
  const [members, setMembers] = useState<Member[]>();
  const [credential, setCredential] = useState("");
  const [revoke, setRevoke] = useState<Member>();
  const [error, setError] = useState("");
  const [generation, setGeneration] = useState(0);

  useEffect(() => {
    const abort = new AbortController();
    void request<Member[]>("members", { signal: abort.signal })
      .then((rows) => {
        setMembers(rows);
        setError("");
      })
      .catch((e: unknown) => {
        if (!abort.signal.aborted) setError(messageOf(e));
      });
    return () => abort.abort();
  }, [generation]);

  async function add(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = e.currentTarget;
    const fields = new FormData(form);
    if (
      await run(async () => {
        const result = await post<{ credential: string }>("members", {
          name: String(fields.get("name")).trim(),
          role: fields.get("role"),
          projectId: fields.get("projectId") || null,
        });
        setCredential(result.credential);
      }, "Workspace access created.")
    ) {
      form.reset();
      setGeneration((n) => n + 1);
    }
  }

  return (
    <Panel title="Workspace access">
      <div className="panel-body">
        {error ? (
          <>
            <p role="alert">{error}</p>
            <button
              disabled={locked}
              onClick={() => setGeneration((n) => n + 1)}
            >
              Reload members
            </button>
          </>
        ) : members ? (
          members.length ? (
            <ul>
              {members.map((member) => (
                <li key={member.id}>
                  {member.name} · {human(member.role)} ·{" "}
                  {projects.find((p) => p.id === member.projectId)?.name ||
                    (member.projectId
                      ? "Selected project"
                      : "All projects")}{" "}
                  {member.revokedAt ? (
                    "· Revoked"
                  ) : (
                    <button
                      disabled={locked}
                      className="quiet"
                      onClick={() => setRevoke(member)}
                    >
                      Revoke access
                    </button>
                  )}
                </li>
              ))}
            </ul>
          ) : (
            <p>No named members.</p>
          )
        ) : (
          <p>Loading members…</p>
        )}
        {members?.some((m) => m.role === "approver" && !m.revokedAt) && (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              const f = new FormData(e.currentTarget);
              const provider = String(f.get("provider"));
              void run(
                () =>
                  post("members/" + enc(String(f.get("member"))) + "/chat", {
                    provider,
                    user: String(f.get("user")).trim(),
                    tenant:
                      provider === "telegram"
                        ? "private"
                        : String(f.get("team")).trim(),
                    remove: f.get("remove") === "on",
                  }),
                "Chat identity updated.",
              );
            }}
          >
            <h3>Chat decisions</h3>
            <Field title="Approver">
              <select name="member">
                {members
                  .filter((m) => m.role === "approver" && !m.revokedAt)
                  .map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.name}
                    </option>
                  ))}
              </select>
            </Field>
            <Field title="Provider">
              <select name="provider">
                <option value="slack">Slack</option>
                <option value="telegram">Telegram</option>
              </select>
            </Field>
            <Field title="Slack team ID">
              <input name="team" />
            </Field>
            <Field title="Provider user ID">
              <input name="user" required />
            </Field>
            <label>
              <input type="checkbox" name="remove" /> Unlink this identity
            </label>
            <button disabled={locked}>Save chat identity</button>
          </form>
        )}
        <form onSubmit={add}>
          <div className="form-pair">
            <Field title="Name">
              <input name="name" required maxLength={100} />
            </Field>
            <Field title="Role">
              <select name="role">
                <option value="operator">Operator</option>
                <option value="approver">Approver</option>
              </select>
            </Field>
          </div>
          <Field title="Project access">
            <select name="projectId">
              <option value="">All projects</option>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </Field>
          <p>
            Operators can report incidents and inspect PRs. Approvers can also
            approve plans and dispatch decisions and record PR evidence. Access
            applies to the selected projects.
          </p>
          <button disabled={locked || !members}>Create access key</button>
        </form>
      </div>
      {credential && (
        <Modal title="Workspace access key" close={() => setCredential("")}>
          <p>
            Share this key privately with the named member. It is shown once.
          </p>
          <Field title="Access key">
            <input
              readOnly
              value={credential}
              onFocus={(e) => e.currentTarget.select()}
            />
          </Field>
          <button onClick={() => setCredential("")}>Done</button>
        </Modal>
      )}
      {revoke && (
        <Modal
          title="Revoke workspace access?"
          close={() => setRevoke(undefined)}
        >
          <p>This signs {revoke.name} out and disables their access key.</p>
          <button
            disabled={locked}
            onClick={async () => {
              if (
                await run(
                  () => post("members/" + enc(revoke.id) + "/revoke", {}),
                  "Workspace access revoked.",
                )
              ) {
                setRevoke(undefined);
                setGeneration((n) => n + 1);
              }
            }}
          >
            Revoke {revoke.name}’s access
          </button>
        </Modal>
      )}
    </Panel>
  );
}
