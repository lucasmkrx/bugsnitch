// SPDX-License-Identifier: MPL-2.0
import { useEffect, useRef, useState } from "react";
import { enc, messageOf, post, request, type Project, type State } from "./api";
import type { Run } from "./actions";
import { Field, Panel, human, link } from "./ui";

interface BoardIssue {
  number: number;
  title: string;
  status: string;
  itemId: string;
}
interface BoardPage {
  number: number;
  title: string;
  issues: BoardIssue[];
  nextCursor: string | null;
}
interface WorkerStart {
  id: string;
  deviceId: string;
  issueNumber: number;
  planId?: string;
  status: string;
  attemptId?: string;
  reason?: string;
}
interface BoardSync {
  issueNumber: number;
  desired: "active" | "done";
  applied: boolean;
  reason?: string;
}

export function WorkerStarts({
  state,
  project,
  locked,
  run,
}: {
  state: State;
  project?: Project;
  locked: boolean;
  run: Run;
}) {
  const [picked, setPicked] = useState("");
  const repository =
    project ||
    state.projects.find((p) => p.id === picked) ||
    (state.projects.length === 1 ? state.projects[0] : undefined);
  return (
    <Panel title="Start a worker">
      <div className="panel-body">
        {!project && state.projects.length !== 1 && (
          <Field title="Repository">
            <select
              value={repository?.id || ""}
              onChange={(event) => setPicked(event.target.value)}
            >
              <option value="">Choose a repository</option>
              {state.projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.repository}
                </option>
              ))}
            </select>
          </Field>
        )}
        {repository && (
          <ProjectWorker
            key={repository.id}
            project={repository}
            state={state}
            locked={locked}
            run={run}
          />
        )}
      </div>
    </Panel>
  );
}

function ProjectWorker({
  state,
  project,
  locked,
  run,
}: {
  state: State;
  project: Project;
  locked: boolean;
  run: Run;
}) {
  const base = "projects/" + enc(project.id);
  const [boardNumber, setBoardNumber] = useState("");
  const [board, setBoard] = useState<BoardPage>();
  const [issueNumber, setIssueNumber] = useState("");
  const [pickedDevice, setPickedDevice] = useState("");
  const [readingBoard, setReadingBoard] = useState(false);
  const [boardError, setBoardError] = useState("");
  const [starts, setStarts] = useState<WorkerStart[]>([]);
  const [boardSync, setBoardSync] = useState<BoardSync[]>([]);
  const [startError, setStartError] = useState("");
  const [received, setReceived] = useState<WorkerStart>();
  const readingStarts = useRef<AbortController | null>(null);
  const intent = useRef({ key: "", id: "" });
  const issue = board?.issues.find((i) => i.number === Number(issueNumber));
  const devices = state.devices.filter(
    (d) => !d.revoked && d.projects.includes(project.id),
  );
  const device =
    devices.find((d) => d.id === pickedDevice) ||
    (devices.length === 1 ? devices[0] : undefined);
  const approved = state.plans
    .filter((p) => p.projectId === project.id && p.status === "approved")
    .sort((a, b) => b.version - a.version)[0];
  const plan = approved?.document.tickets?.some(
    (t) => t.number === issue?.number,
  )
    ? approved
    : undefined;
  const receipts =
    received && !starts.some((s) => s.id === received.id)
      ? [received, ...starts]
      : starts;
  const pending = receipts.find(
    (s) =>
      s.issueNumber === issue?.number &&
      ["queued", "planning", "awaiting_plan", "starting"].includes(s.status),
  );
  const running = state.attempts.find(
    (a) =>
      a.projectId === project.id &&
      a.issueNumber === issue?.number &&
      ["running", "waiting"].includes(a.state),
  );

  useEffect(() => {
    return () => {
      readingStarts.current?.abort();
      readingStarts.current = null;
    };
  }, [base]);
  useEffect(() => {
    if (readingStarts.current) return;
    const controller = new AbortController();
    readingStarts.current = controller;
    void request<{ starts: WorkerStart[]; boardSync?: BoardSync[] }>(
      base + "/worker-starts",
      {
        signal: controller.signal,
      },
    )
      .then((result) => {
        if (!controller.signal.aborted) {
          setStarts(result.starts || []);
          setReceived((prior) =>
            result.starts?.some((s) => s.id === prior?.id) ? undefined : prior,
          );
          setBoardSync(result.boardSync || []);
          setStartError("");
        }
      })
      .catch((error) => {
        if (!controller.signal.aborted) setStartError(messageOf(error));
      })
      .finally(() => {
        if (readingStarts.current === controller) readingStarts.current = null;
      });
  }, [base, state]);

  async function loadBoard(more = false) {
    const number = Number(boardNumber);
    if (
      readingBoard ||
      !Number.isSafeInteger(number) ||
      number < 1 ||
      (more && !board?.nextCursor)
    )
      return;
    setReadingBoard(true);
    setBoardError("");
    try {
      const page = await post<BoardPage>(base + "/github-project/issues", {
        number,
        ...(more ? { after: board?.nextCursor } : {}),
      });
      const unique = new Map(
        (more ? board?.issues || [] : []).map((i) => [i.number, i]),
      );
      for (const item of page.issues) unique.set(item.number, item);
      setBoard({ ...page, issues: [...unique.values()] });
      if (!more) setIssueNumber("");
    } catch (error) {
      setBoardError(messageOf(error));
    } finally {
      setReadingBoard(false);
    }
  }

  async function start() {
    if (
      !board ||
      !issue ||
      !device ||
      locked ||
      pending ||
      running ||
      project.paused ||
      state.workspace.stopped
    )
      return;
    const binding = {
      deviceId: device.id,
      planId: plan?.id || "",
      policyVersion: project.policyVersion,
    };
    const key = JSON.stringify([issue.number, binding]);
    if (intent.current.key !== key)
      intent.current = { key, id: crypto.randomUUID() };
    const id = intent.current.id;
    const ok = await run(async () => {
      await post(base + "/github-project/import", {
        number: board.number,
        issue: issue.number,
        itemId: issue.itemId,
      });
      const response = await post<WorkerStart>(
        base + "/issues/" + issue.number + "/start",
        {
          requestId: id,
          ...binding,
        },
      );
      setReceived(response);
    }, "Worker start requested.");
    if (ok) intent.current = { key: "", id: "" };
  }

  return (
    <>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void loadBoard();
        }}
      >
        <Field title="GitHub Project number">
          <input
            required
            type="number"
            min="1"
            step="1"
            value={boardNumber}
            disabled={locked || readingBoard}
            onChange={(e) => {
              setBoardNumber(e.target.value);
              setBoard(undefined);
              setIssueNumber("");
              setBoardError("");
            }}
          />
        </Field>
        <button disabled={locked || readingBoard || !boardNumber}>
          {readingBoard ? "Loading tickets…" : "Load tickets"}
        </button>
      </form>
      {boardError && (
        <p className="alert" role="alert">
          {boardError}
        </p>
      )}
      {board && (
        <>
          <p>
            {board.title} · {project.repository}
          </p>
          <Field title="Ticket">
            <select
              aria-label="Ticket"
              value={issueNumber}
              disabled={locked || readingBoard}
              onChange={(e) => setIssueNumber(e.target.value)}
            >
              <option value="">Choose an open issue</option>
              {board.issues.map((i) => (
                <option value={i.number} key={i.number}>
                  #{i.number} · {i.title}
                  {i.status ? " · " + i.status : ""}
                </option>
              ))}
            </select>
          </Field>
          {!board.issues.length && (
            <p>No open issues for {project.repository} on this page.</p>
          )}
          {board.nextCursor && (
            <button
              disabled={locked || readingBoard}
              onClick={() => void loadBoard(true)}
            >
              Load more tickets
            </button>
          )}
        </>
      )}
      {issue && (
        <>
          <Field title="Assigned runner">
            <select
              aria-label="Assigned runner"
              value={device?.id || ""}
              disabled={locked}
              onChange={(e) => setPickedDevice(e.target.value)}
            >
              <option value="">Choose a computer</option>
              {devices.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name}
                </option>
              ))}
            </select>
          </Field>
          {!devices.length && (
            <p>
              <a href={link("devices", project.id)}>Pair a computer</a> and
              assign this repository.
            </p>
          )}
          {state.workspace.stopped || project.paused ? (
            <p>Dispatch is paused.</p>
          ) : running ? (
            <p>
              <a href={link("work", project.id, running.id)}>
                View current attempt
              </a>
            </p>
          ) : pending ? (
            <p role="status">
              Worker start {human(pending.status).toLowerCase()}.
            </p>
          ) : (
            <>
              {!plan && (
                <p>The runner will draft a plan and estimate for approval.</p>
              )}
              <button
                className="primary"
                disabled={locked || !device}
                onClick={() => void start()}
              >
                Start worker
              </button>
            </>
          )}
        </>
      )}
      {startError && (
        <p role="status">Worker starts unavailable. {startError}</p>
      )}
      {receipts.length > 0 && (
        <>
          <h3>Recent worker starts</h3>
          <ul>
            {receipts.map((s) => (
              <li key={s.id}>
                #{s.issueNumber} ·{" "}
                {state.devices.find((d) => d.id === s.deviceId)?.name ||
                  s.deviceId}{" "}
                · {human(s.status)}
                {s.reason && " · " + s.reason}
                {s.status === "awaiting_plan" && s.planId && (
                  <>
                    {" "}
                    ·{" "}
                    <a href={link("plans", project.id, s.planId)}>
                      Review proposed plan
                    </a>
                  </>
                )}
                {s.attemptId && (
                  <>
                    {" "}
                    ·{" "}
                    <a href={link("work", project.id, s.attemptId)}>
                      View attempt
                    </a>
                  </>
                )}
              </li>
            ))}
          </ul>
        </>
      )}
      {boardSync.length > 0 && (
        <>
          <h3>GitHub Projects updates</h3>
          <ul>
            {boardSync.map((item) => (
              <li key={item.issueNumber}>
                <a
                  href={`https://github.com/${project.repository}/issues/${item.issueNumber}`}
                >
                  #{item.issueNumber}
                </a>
                {" · " +
                  human(item.desired) +
                  (item.applied ? " synced" : " update pending")}
                {!item.applied && item.reason && " · " + item.reason}
              </li>
            ))}
          </ul>
        </>
      )}
    </>
  );
}
