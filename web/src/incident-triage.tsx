// SPDX-License-Identifier: MPL-2.0
import { useState, type FormEvent } from "react";
import { enc, messageOf, post, request, type Incident } from "./api";
import type { Run } from "./actions";
import { Field } from "./ui";

interface Suggestion {
  number: number;
  title: string;
  pull: boolean;
  match: string[];
}

export function RelatedIncidentWork({
  incident,
  repository,
  locked,
  canLink,
  run,
}: {
  incident: Incident;
  repository: string;
  locked: boolean;
  canLink: boolean;
  run: Run;
}) {
  const [items, setItems] = useState<Suggestion[]>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const path = `projects/${enc(incident.projectId)}/incidents/${enc(incident.id)}`;
  async function find() {
    setError("");
    setLoading(true);
    try {
      setItems(
        (await request<{ items: Suggestion[] }>(`${path}/related-work`)).items,
      );
    } catch (error) {
      setError(messageOf(error));
    } finally {
      setLoading(false);
    }
  }
  function linkIssue(number: number) {
    void run(
      () => post(`${path}/link-issue`, { number, version: incident.version }),
      `Linked GitHub issue #${number}.`,
    );
  }
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    linkIssue(Number(new FormData(e.currentTarget).get("issue")));
  }
  return (
    <div className="panel-body">
      <h3>Related GitHub work</h3>
      <button
        type="button"
        disabled={locked || loading}
        onClick={() => void find()}
      >
        {loading
          ? "Looking up related work…"
          : items
            ? "Refresh related work"
            : "Find related work"}
      </button>
      {error && <p role="alert">{error}</p>}
      {items &&
        (items.length ? (
          <ul className="related-work">
            {items.map((item) => (
              <li key={item.number}>
                <a
                  href={`https://github.com/${repository}/${item.pull ? "pull" : "issues"}/${item.number}`}
                >
                  {item.pull ? "PR" : "Issue"} #{item.number} {item.title}
                </a>
                <small>Matches {item.match.join(", ")}</small>
                {canLink && !item.pull && (
                  <button
                    type="button"
                    disabled={locked}
                    onClick={() => linkIssue(item.number)}
                  >
                    Link issue #{item.number}
                  </button>
                )}
              </li>
            ))}
          </ul>
        ) : (
          <p role="status">
            No close matches among the repository’s recently updated open issues
            and pull requests.
          </p>
        ))}
      {canLink && (
        <details>
          <summary>Link an issue by number</summary>
          <form onSubmit={submit}>
            <Field title="Existing GitHub issue number">
              <input
                name="issue"
                type="number"
                min={1}
                max={2147483647}
                required
              />
            </Field>
            <button disabled={locked}>Link existing issue</button>
          </form>
        </details>
      )}
    </div>
  );
}
