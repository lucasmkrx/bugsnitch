// SPDX-License-Identifier: MPL-2.0
import { useRef, useState, type ReactNode } from "react";
import {
  type State,
  type Project,
  type Activity,
  type ProgressForecast,
  post,
  enc,
} from "./api";
import {
  Icon,
  Panel,
  Empty,
  Badge,
  Field,
  human,
  when,
  link,
  type Page,
  type IconName,
} from "./ui";
import { DispatchControl, SharedDiff, DraftPR } from "./controls";
import { Settings } from "./settings";
import { Publications } from "./publications";
import { Pulls, Verification } from "./evidence";
import { type ModalKind, type Run, decide } from "./actions";
import {
  AttemptAction,
  LinearLinks,
  attemptAction,
  ticketAction,
} from "./work";
import { Members } from "./members";
import { WorkerStarts } from "./worker-starts";
import { ModelAccess } from "./model-access";
import { RelatedIncidentWork } from "./incident-triage";
import { IncidentBrowserQA } from "./browser-qa";

interface Props {
  page: Page;
  state: State;
  project?: Project;
  subject: string;
  run: Run;
  locked: boolean;
  open: (kind: ModalKind) => void;
}
const nav = (page: Page, project?: Project, id?: string) =>
  link(page, project?.id, id);
const fmt = (s?: string | null) => s || "Unknown";
const match = (query: string, ...fields: (string | number | undefined)[]) =>
  fields.join(" ").toLowerCase().includes(query.trim().toLowerCase());
function ActionLink({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a className="text-link" href={href}>
      {children}
      <Icon name="arrow" size={15} />
    </a>
  );
}
const terminalStates = new Set([
  "completed",
  "cancelled",
  "failed",
  "stopped",
  "ready",
  "ready_for_pr",
]);
function attemptStateLabel(state: string) {
  return state === "completed" ? "Local candidate ready" : human(state);
}
function executionLabel(a: { state: string; connection: string }) {
  return a.connection === "disconnected" && !terminalStates.has(a.state)
    ? "State unknown"
    : attemptStateLabel(a.state);
}
function Row({
  icon,
  heading,
  detail,
  tail,
  href,
}: {
  icon: IconName;
  heading: string;
  detail?: string;
  tail?: ReactNode;
  href?: string;
}) {
  const contents = (
    <>
      <span className="row-icon">
        <Icon name={icon} />
      </span>
      <span className="row-txt">
        <strong>{heading}</strong>
        {detail && <small>{detail}</small>}
      </span>
      {tail && <span className="row-tail">{tail}</span>}
    </>
  );
  return href ? (
    <a href={href} className="row linked">
      {contents}
    </a>
  ) : (
    <div className="row">{contents}</div>
  );
}
function note(title: string, description: string, icon: IconName) {
  return (
    <Empty title={title} icon={icon}>
      {description}
    </Empty>
  );
}
function eventName(k: string) {
  return k.split(".").map(human).join(" · ");
}
function eventUrl(e: Activity) {
  const section = e.kind.split(".")[0],
    p = e.projectId || "";
  switch (section) {
    case "incident":
      return link("incidents", p, e.subjectId);
    case "attempt":
      return link("work", p, e.subjectId);
    case "decision":
      return link("decisions", p, e.subjectId);
    case "project":
      return link("settings", p);
    case "device":
      return link("devices");
    case "message":
      return link("chat", p);
    case "plan":
      return link("plans", p, e.subjectId);
    default:
      return undefined;
  }
}
function forecastRows(forecast?: ProgressForecast): [string, ReactNode][] {
  if (!forecast) return [["Progress forecast", "No forecast data received."]];
  const rows: [string, ReactNode][] = [];
  if (forecast.status === "unknown") {
    rows.push([
      "Progress forecast",
      forecast.reason ||
        "Insufficient current integration evidence to estimate a finish date.",
    ]);
  } else {
    const delta = forecast.targetDeltaDays;
    const distance =
      delta == null
        ? ""
        : Math.abs(delta) + (Math.abs(delta) === 1 ? " day" : " days");
    const comparison =
      delta == null
        ? ""
        : delta === 0
          ? " · On target date"
          : " · " + distance + (delta > 0 ? " after target" : " before target");
    rows.push([
      forecast.status === "complete"
        ? "Recorded completion"
        : "Projected finish",
      <>
        {fmt(forecast.date)}
        {comparison}
      </>,
    ]);
  }
  rows.push([
    "Recorded progress",
    forecast.completedTickets +
      " of " +
      forecast.totalTickets +
      " tickets with integration evidence as of " +
      forecast.asOf +
      " UTC.",
  ]);
  if (forecast.status === "projected")
    rows.push([
      "Forecast basis",
      "Observed delivery pace since approval, weighted by ticket estimates (" +
        forecast.completedEstimateHours +
        " of " +
        forecast.totalEstimateHours +
        " estimated hours represented).",
    ]);
  return rows;
}
function Detail({ pairs }: { pairs: [string, ReactNode][] }) {
  return (
    <dl className="detail">
      {pairs.map(([k, v]) => (
        <div key={k}>
          <dt>{k}</dt>
          <dd>{v}</dd>
        </div>
      ))}
    </dl>
  );
}

export function Views({
  page,
  state: s,
  project,
  subject,
  run,
  locked: offlineLocked,
  open,
}: Props) {
  const role = s.viewer?.role || "owner";
  const locked = offlineLocked || role !== "owner";
  const approvalsLocked = offlineLocked || role === "operator";
  const [query, setQuery] = useState(""),
    [status, setStatus] = useState("all"),
    [picked, setPicked] = useState(""),
    [chat, setChat] = useState("");
  const [confirmRevoke, setConfirmRevoke] = useState("");
  const chatKey = useRef(crypto.randomUUID());
  const scope = <T extends { projectId: string }>(rows: T[]) =>
    project ? rows.filter((r) => r.projectId === project.id) : rows;
  const projects = project ? [project] : s.projects,
    incidents = scope(s.incidents),
    decisions = scope(s.decisions),
    tickets = scope(s.tickets),
    attempts = scope(s.attempts),
    plans = scope(s.plans),
    messages = scope(s.messages);
  const eligibleDecisions = decisions.filter((d) => d.status === "pending");
  const inProgress = attempts.filter((a) => !terminalStates.has(a.state));
  const active = inProgress.filter(
    (a) => a.connection === "connected" && a.state === "running",
  );
  const devices = project
    ? s.devices.filter((d) => d.projects.includes(project.id))
    : s.devices;
  const events = project
    ? s.events.filter((e) => e.projectId === project.id)
    : s.events;
  const name = (id: string) =>
    s.projects.find((p) => p.id === id)?.name || "Workspace";
  const pRef = (id: string) => s.projects.find((p) => p.id === id);
  const selected = s.projects.find(
    (p) => p.id === (project?.id || picked || s.projects[0]?.id),
  );
  function search(placeholder: string, options?: string[]) {
    return (
      <div className="filters">
        <label className="search">
          <Icon name="search" size={17} />
          <input
            aria-label={placeholder}
            placeholder={placeholder}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </label>
        {options && (
          <select
            aria-label="Filter by status"
            value={status}
            onChange={(e) => setStatus(e.target.value)}
          >
            <option value="all">All statuses</option>
            {[...new Set(options)].map((x) => (
              <option key={x} value={x}>
                {human(x)}
              </option>
            ))}
          </select>
        )}
      </div>
    );
  }
  const filtered = (state: string) => status === "all" || status === state;
  const back = (section: Page) => (
    <a className="text-link" href={nav(section, project)}>
      <Icon name="back" size={15} />
      Back to list
    </a>
  );
  if (page === "overview") {
    const latestIncidents = [...incidents]
      .sort(
        (a, b) =>
          b.occurrences - a.occurrences ||
          Date.parse(b.lastSeen) - Date.parse(a.lastSeen),
      )
      .slice(0, 3);
    const metrics: [string, number, Page, IconName][] = [
      ["Active attempts", active.length, "work", "work"],
      ["Pending decisions", eligibleDecisions.length, "decisions", "decisions"],
      ["Incidents", incidents.length, "incidents", "incidents"],
      [
        "Registered devices",
        devices.filter((d) => !d.revoked).length,
        "devices",
        "devices",
      ],
    ];
    return (
      <>
        <div className="metrics">
          {metrics.map(([label, value, to, icon]) => (
            <a className="metric" key={label} href={nav(to, project)}>
              <span>
                <Icon name={icon} />
                {label}
              </span>
              <strong>{value}</strong>
            </a>
          ))}
        </div>
        <div className="columns">
          <div className="stack">
            <Panel
              title="Workspace activity"
              action={
                <span className="subtle">
                  <Icon name="clock" size={14} />
                  Latest first
                </span>
              }
            >
              {events.length ? (
                <div className="timeline">
                  {events.slice(0, 8).map((e) => (
                    <Row
                      key={e.seq}
                      icon="activity"
                      heading={eventName(e.kind)}
                      detail={
                        e.projectId ? name(e.projectId) : s.workspace.name
                      }
                      href={eventUrl(e)}
                      tail={<time>{when(e.createdAt)}</time>}
                    />
                  ))}
                </div>
              ) : (
                note(
                  "Activity will appear here",
                  "Register a project to start the workspace history.",
                  "activity",
                )
              )}
            </Panel>
            <Panel
              title="In progress"
              action={
                <ActionLink href={nav("work", project)}>View work</ActionLink>
              }
            >
              {inProgress.length
                ? inProgress
                    .slice(0, 4)
                    .map((a) => (
                      <Row
                        key={a.id}
                        icon="work"
                        heading={a.repository + " #" + a.issueNumber}
                        detail={human(a.phase) + " · " + a.machine}
                        tail={<Badge>{executionLabel(a)}</Badge>}
                        href={link("work", a.projectId, a.id)}
                      />
                    ))
                : note(
                    "No active attempts",
                    "Attempts appear when an assigned computer synchronizes local work.",
                    "work",
                  )}
            </Panel>
            {latestIncidents.length > 0 && (
              <Panel
                title="Incident activity"
                action={
                  <ActionLink href={nav("incidents", project)}>
                    View incidents
                  </ActionLink>
                }
              >
                {latestIncidents.map((i) => (
                  <Row
                    key={i.id}
                    icon="incidents"
                    heading={i.title || i.observed}
                    detail={
                      i.occurrences + " occurrences · " + name(i.projectId)
                    }
                    href={link("incidents", i.projectId, i.id)}
                    tail={<Badge>{human(i.status)}</Badge>}
                  />
                ))}
              </Panel>
            )}
          </div>
          <div className="stack">
            <Panel
              title="Needs your decision"
              action={<span className="count">{eligibleDecisions.length}</span>}
            >
              {eligibleDecisions.length
                ? eligibleDecisions
                    .slice(0, 3)
                    .map((d) => (
                      <Row
                        key={d.id}
                        icon="decisions"
                        heading={d.question}
                        detail={name(d.projectId)}
                        href={link("decisions", d.projectId, d.id)}
                      />
                    ))
                : note(
                    "No decisions pending",
                    "Requests for your approval will appear here.",
                    "decisions",
                  )}
            </Panel>
            <Panel
              title="Projects"
              action={
                <button
                  className="icon"
                  aria-label="Register a project"
                  disabled={locked}
                  onClick={() => open("project")}
                >
                  <Icon name="plus" />
                </button>
              }
            >
              {projects.length ? (
                projects
                  .slice(0, 6)
                  .map((p) => (
                    <Row
                      key={p.id}
                      icon="repo"
                      heading={p.name}
                      detail={p.repository}
                      href={link("settings", p.id)}
                      tail={
                        p.paused ? (
                          <Badge tone="warn">Paused</Badge>
                        ) : (
                          <Icon name="arrow" size={16} />
                        )
                      }
                    />
                  ))
              ) : (
                <div className="panel-body">
                  <p>
                    Connect your first repository to organize work, incidents,
                    and decisions.
                  </p>
                  <button disabled={locked} onClick={() => open("project")}>
                    Register project
                  </button>
                </div>
              )}
            </Panel>
          </div>
        </div>
      </>
    );
  }
  if (page === "work") {
    const a = attempts.find((a) => a.id === subject);
    if (subject && !a)
      return (
        <Panel title="Attempt">
          {back("work")}
          {note(
            "Attempt unavailable",
            "Check the selected project or return to the work list.",
            "work",
          )}
        </Panel>
      );
    if (a) {
      const canControl = !terminalStates.has(a.state);
      return (
        <>
          {back("work")}
          <Panel
            title={
              <a
                href={`https://github.com/${a.repository}/issues/${a.issueNumber}`}
              >
                {a.repository} #{a.issueNumber}
              </a>
            }
            action={<Badge>{executionLabel(a)}</Badge>}
          >
            <Detail
              pairs={[
                ["Last reported phase", human(a.phase)],
                ["Last reported state", attemptStateLabel(a.state)],
                [
                  "Execution reason",
                  a.reasonCode ? human(a.reasonCode) : "None reported",
                ],
                ["Assigned computer", a.machine],
                ["Contact", human(a.connection)],
                ["Last report", when(a.updatedAt)],
                ["Base revision", <code>{a.baseSha}</code>],
                ["Candidate revision", <code>{fmt(a.candidateSha)}</code>],
                ["Attempt ID", <code>{a.id}</code>],
              ]}
            />
            <AttemptAction
              key={a.id + ":" + a.version}
              state={s}
              attempt={a}
              run={run}
              locked={offlineLocked}
            />
            <div className="panel-body">
              <h3>Execution evidence</h3>
              {a.evidence?.length ? (
                a.evidence.map((e, index) => (
                  <details key={index}>
                    <summary>
                      {human(e.phase)}{" "}
                      {e.exitCode != null ? "· Exit " + e.exitCode : ""}
                    </summary>
                    <Detail
                      pairs={[
                        [
                          "Candidate revision",
                          <code>{fmt(e.candidateSha)}</code>,
                        ],
                        ["Changed files", e.changedFiles],
                        ["Important findings", e.importantFindings],
                        ["Minor findings", e.minorFindings],
                        ...(e.durationMs == null
                          ? []
                          : [
                              [
                                "Elapsed time",
                                (e.durationMs / 1000).toLocaleString(
                                  undefined,
                                  { maximumFractionDigits: 3 },
                                ) + " s",
                              ] as [string, ReactNode],
                            ]),
                      ]}
                    />
                  </details>
                ))
              ) : (
                <p>No stage evidence synchronized yet.</p>
              )}
              <p>Full artifacts are retained on {a.machine}.</p>
              {a.control && (
                <p role="status">
                  Last control · {human(a.control.operation)} ·{" "}
                  {human(a.control.status)}
                </p>
              )}
              <div className="actions">
                {(["pause", "stop"] as const).map((op) => (
                  <button
                    key={op}
                    disabled={
                      locked ||
                      !canControl ||
                      ["requested", "received"].includes(
                        a.control?.status || "",
                      )
                    }
                    onClick={() =>
                      void run(
                        () =>
                          post("attempts/" + enc(a.id) + "/control", {
                            operation: op,
                            version: a.version,
                            requestId: crypto.randomUUID(),
                          }),
                        "Control request recorded. Check the runner acknowledgment below.",
                      )
                    }
                  >
                    Request {op}
                  </button>
                ))}
              </div>
            </div>
          </Panel>
          <SharedDiff attempt={a} />
          <DraftPR
            attempt={a}
            project={pRef(a.projectId)}
            state={s}
            locked={locked}
            canInspect={!offlineLocked}
            run={run}
          />
        </>
      );
    }
    const rows = attempts.filter((a) =>
      match(
        query,
        a.repository,
        a.issueNumber,
        a.machine,
        a.phase,
        a.state,
        attemptStateLabel(a.state),
        a.reasonCode,
      ),
    );
    const items = tickets.filter((t) =>
      match(query, t.repository, t.number, t.title, t.state),
    );
    const latest = new Map<string, (typeof attempts)[number]>();
    for (const a of attempts) {
      const key = a.projectId + ":" + a.issueNumber;
      const prior = latest.get(key);
      if (
        !prior ||
        (terminalStates.has(prior.state) && !terminalStates.has(a.state))
      ) {
        latest.set(key, a);
      }
    }
    return (
      <>
        {role === "owner" && !s.capabilities.demo && (
          <WorkerStarts state={s} project={project} locked={locked} run={run} />
        )}
        {search("Search issues and attempts")}
        <Panel title="Attempts">
          {rows.length
            ? rows.map((a) => (
                <Row
                  key={a.id}
                  icon="work"
                  heading={a.repository + " #" + a.issueNumber}
                  detail={
                    human(a.phase) +
                    " · " +
                    a.machine +
                    " · " +
                    when(a.updatedAt) +
                    " · " +
                    attemptAction(s, a).label
                  }
                  tail={
                    a.connection === "disconnected" &&
                    a.state !== "completed" ? (
                      <Badge tone="warn">Disconnected</Badge>
                    ) : (
                      <Badge>{attemptStateLabel(a.state)}</Badge>
                    )
                  }
                  href={link("work", a.projectId, a.id)}
                />
              ))
            : note(
                query ? "No matching attempts" : "No attempts reported",
                "Runner stage updates appear after synchronization.",
                "work",
              )}
        </Panel>
        <Panel title="Canonical issues">
          {items.length
            ? items.map((t) => {
                const action = ticketAction(
                  s,
                  t,
                  latest.get(t.projectId + ":" + t.number),
                );
                return (
                  <Row
                    key={t.id}
                    icon="repo"
                    heading={t.title}
                    detail={
                      t.repository + " #" + t.number + " · " + action.label
                    }
                    href={
                      action.href ||
                      "https://github.com/" +
                        t.repository +
                        "/issues/" +
                        t.number
                    }
                    tail={<Badge>{human(t.state || "unknown")}</Badge>}
                  />
                );
              })
            : note(
                query ? "No matching issues" : "No linked issues",
                "Select a ticket from GitHub Projects or promote a qualifying incident.",
                "repo",
              )}
        </Panel>
        <LinearLinks
          tickets={tickets}
          run={run}
          locked={locked}
          canLink={role === "owner" && tickets.some((t) => t.linear)}
        />
      </>
    );
  }
  if (page === "incidents") {
    const i = incidents.find((i) => i.id === subject);
    if (subject && !i)
      return (
        <>
          {back("incidents")}
          {note(
            "Incident unavailable",
            "Return to the list to review the current records.",
            "incidents",
          )}
        </>
      );
    if (i) {
      const p = pRef(i.projectId);
      return (
        <>
          {back("incidents")}
          <Panel
            title={i.title || i.observed}
            action={<Badge>{human(i.status)}</Badge>}
          >
            <Detail
              pairs={[
                ["Project", name(i.projectId)],
                ["Source", human(i.source)],
                ["Observed behavior", i.observed],
                ["Expected behavior", fmt(i.expected)],
                ["Reproduction steps", fmt(i.steps)],
                ["Occurrences", i.occurrences],
                ["First seen", when(i.firstSeen)],
                ["Last seen", when(i.lastSeen)],
                ["Environment", fmt(i.environment)],
                ["Release", fmt(i.release)],
                [
                  "GitHub issue",
                  i.issueNumber && p ? (
                    <a
                      href={
                        "https://github.com/" +
                        p.repository +
                        "/issues/" +
                        i.issueNumber
                      }
                    >
                      {p.repository} #{i.issueNumber}
                    </a>
                  ) : (
                    "Awaiting canonical ticket"
                  ),
                ],
                [
                  "Issue promotion",
                  i.promotion ? human(i.promotion) : "Awaiting owner triage",
                ],
              ]}
            />
            {i.promotionReason && (
              <p className="panel-body">{i.promotionReason}</p>
            )}
            {i.frames?.length ? (
              <details>
                <summary>Sanitized stack frames</summary>
                <pre>{JSON.stringify(i.frames, null, 2)}</pre>
              </details>
            ) : null}
            {!i.issueNumber && !i.promotion && (
              <>
                {p && (
                  <RelatedIncidentWork
                    key={`${i.id}:${i.version}`}
                    incident={i}
                    repository={p.repository}
                    locked={offlineLocked}
                    canLink={role === "owner"}
                    run={run}
                  />
                )}
                <div className="panel-body">
                  <button
                    disabled={locked}
                    onClick={() =>
                      void run(
                        () =>
                          post(
                            "projects/" +
                              enc(i.projectId) +
                              "/incidents/" +
                              enc(i.id) +
                              "/promote",
                            { version: i.version },
                          ),
                        "Issue promotion accepted for processing.",
                      )
                    }
                  >
                    Approve issue promotion
                  </button>
                </div>
              </>
            )}
          </Panel>
          {i.issueNumber && (
            <IncidentBrowserQA
              key={`${i.id}:${i.version}`}
              incident={i}
              locked={offlineLocked}
              canShare={role !== "operator"}
              run={run}
            />
          )}
          <Verification
            key={i.id}
            owner={role === "owner"}
            pulls={s.pullRequests.filter(p=>p.projectId===i.projectId && p.issueNumber===i.issueNumber && p.state==="MERGED" && p.mergedSha)}
            incident={i}
            locked={offlineLocked}
          />
        </>
      );
    }
    const rows = incidents.filter(
      (i) =>
        match(query, i.title, i.observed, i.source, i.release) &&
        filtered(i.status),
    );
    return (
      <>
        {search(
          "Search incidents",
          incidents.map((i) => i.status),
        )}
        <Panel
          title="Incident inbox"
          action={<span className="count">{rows.length}</span>}
        >
          {rows.length
            ? rows.map((i) => (
                <Row
                  key={i.id}
                  icon="incidents"
                  heading={i.title || i.observed}
                  detail={
                    name(i.projectId) +
                    " · " +
                    human(i.source) +
                    " · " +
                    when(i.lastSeen)
                  }
                  href={link("incidents", i.projectId, i.id)}
                  tail={<Badge>{human(i.status)}</Badge>}
                />
              ))
            : note(
                query || status !== "all"
                  ? "No matching incidents"
                  : "No incidents reported",
                "Report observed and expected behavior or connect a Sentry project.",
                "incidents",
              )}
        </Panel>
      </>
    );
  }
  if (page === "decisions") {
    const rows = decisions.filter(
      (d) =>
        (!subject || d.id === subject) &&
        filtered(d.status) &&
        match(query, d.question, name(d.projectId)),
    );
    return (
      <>
        {subject
          ? back("decisions")
          : search(
              "Search decisions",
              decisions.map((d) => d.status),
            )}
        {rows.length ? (
          rows.map((d) => (
            <Panel
              key={d.id}
              title={d.question}
              action={
                <Badge tone={d.status === "pending" ? "warn" : "neutral"}>
                  {human(d.status)}
                </Badge>
              }
            >
              <div className="panel-body">
                <p>
                  {name(d.projectId)} · Project version {d.subjectVersion} ·
                  Expires {when(d.expiresAt)}
                </p>
                <p>
                  Resuming dispatch permits new eligible work. Keeping the
                  project paused holds new assignments.
                </p>
                {d.answer && <strong>Recorded answer · {d.answer}</strong>}
                {d.status === "pending" && (
                  <details>
                    <summary>Share decision link</summary>
                    <Field title="Decision link">
                      <input
                        readOnly
                        value={
                          new URL(
                            link("decisions", d.projectId, d.id),
                            window.location.href,
                          ).href
                        }
                        onFocus={(e) => e.currentTarget.select()}
                      />
                    </Field>
                  </details>
                )}
                {d.status === "pending" && (
                  <div className="actions">
                    {d.options.map((option) => (
                      <button
                        key={option}
                        disabled={approvalsLocked}
                        onClick={() =>
                          void run(
                            () => decide(s.workspace.id, d, option),
                            "Decision recorded.",
                          )
                        }
                      >
                        {option}
                      </button>
                    ))}
                  </div>
                )}
              </div>
            </Panel>
          ))
        ) : (
          <Panel title="Decision queue">
            {note(
              "No decisions in this view",
              "Decisions are tied to the exact project version under review.",
              "decisions",
            )}
          </Panel>
        )}
      </>
    );
  }
  if (page === "devices") {
    const rows = devices.filter((d) => match(query, d.name, d.platform));
    return (
      <>
        {search("Search devices")}
        <Panel title="Registered computers">
          {rows.length
            ? rows.map((d) => {
                const reports = attempts.filter((a) => a.deviceId === d.id);
                const owned = reports.filter(
                  (a) => !terminalStates.has(a.state),
                );
                return (
                  <div key={d.id} className="device">
                    <Row
                      icon="devices"
                      heading={d.name}
                      detail={
                        (d.platform || "Platform unknown") +
                        " · Last contact " +
                        when(d.lastContact)
                      }
                      tail={
                        <Badge tone={d.revoked ? "warn" : "neutral"}>
                          {d.revoked
                            ? "Revoked"
                            : Date.now() - Date.parse(d.lastContact || "") <
                                120000
                              ? "Recently connected"
                              : "Contact unknown"}
                        </Badge>
                      }
                    />
                    <div className="device-detail">
                      <span>
                        {d.projects.map(name).join(", ") ||
                          "No assigned projects"}
                      </span>
                      {owned.length > 0 && (
                        <ActionLink
                          href={link("work", owned[0].projectId, owned[0].id)}
                        >
                          {owned.length === 1
                            ? "1 unresolved attempt"
                            : owned.length + " unresolved attempts"}
                        </ActionLink>
                      )}
                      {!d.revoked &&
                        (confirmRevoke === d.id ? (
                          <div className="confirm">
                            <span>
                              Revoke future authorization on this device?
                            </span>
                            <button
                              disabled={locked}
                              onClick={() =>
                                void run(
                                  () =>
                                    post(
                                      "devices/" + enc(d.id) + "/revoke",
                                      {},
                                    ),
                                  "Device access revoked.",
                                )
                              }
                            >
                              Confirm revocation
                            </button>
                            <button onClick={() => setConfirmRevoke("")}>
                              Cancel
                            </button>
                          </div>
                        ) : (
                          <button
                            className="quiet"
                            disabled={locked}
                            onClick={() => setConfirmRevoke(d.id)}
                          >
                            Revoke access
                          </button>
                        ))}
                    </div>
                  </div>
                );
              })
            : note(
                query ? "No matching devices" : "No registered computers",
                "Pair a computer with a one-time code and select its permitted projects.",
                "devices",
              )}
        </Panel>
      </>
    );
  }
  if (page === "plans") {
    const rows = plans.filter(
      (p) =>
        (!subject || p.id === subject) &&
        match(query, p.title, p.document.milestone),
    );
    return (
      <>
        {subject ? back("plans") : search("Search plans")}
        {!subject && (
          <Publications state={s} project={project} run={run} locked={locked} />
        )}{" "}
        {rows.length ? (
          rows.map((p) => (
            <Panel
              title={p.title}
              key={p.id}
              action={<Badge>{human(p.status)}</Badge>}
            >
              <Detail
                pairs={[
                  ["Project", name(p.projectId)],
                  ["Milestone", p.document.milestone],
                  ["Target date", p.document.target || "No target set"],
                  [
                    "Initial planning estimate",
                    p.document.forecast || "No date supplied",
                  ],
                  ...forecastRows(p.progressForecast),
                  ["Assumptions", fmt(p.document.assumptions)],
                  [
                    "Execution order",
                    (p.executionOrder || []).map((n) => "#" + n).join(" → ") ||
                      "Pending",
                  ],
                ]}
              />
              {p.document.tickets?.map((t) => (
                <details key={t.number}>
                  <summary>
                    #{t.number} · {t.title}
                  </summary>
                  <Detail
                    pairs={[
                      [
                        "Acceptance criteria",
                        t.criteria?.length ? (
                          <ul>
                            {t.criteria.map((c, i) => (
                              <li key={i}>{c}</li>
                            ))}
                          </ul>
                        ) : (
                          "No criteria recorded"
                        ),
                      ],
                      ["Verification", t.verification],
                      ["Estimate", t.estimateHours + " hours"],
                      [
                        "Prerequisites",
                        (t.dependencies || []).join(", ") || "None",
                      ],
                      [
                        "Shared resources",
                        (t.resources || []).join(", ") || "None",
                      ],
                    ]}
                  />
                </details>
              ))}
              {p.status === "proposed" && (
                <div className="panel-body">
                  <button
                    disabled={approvalsLocked}
                    onClick={() =>
                      void run(
                        () =>
                          post("plans/" + enc(p.id) + "/approve", {
                            version: pRef(p.projectId)?.version,
                          }),
                        "Plan approval recorded.",
                      )
                    }
                  >
                    Approve plan
                  </button>
                </div>
              )}
            </Panel>
          ))
        ) : (
          <Panel title="Delivery plans">
            {note(
              "No plans in this view",
              "Draft a deliverable with canonical issue numbers, criteria, and a verification method.",
              "plans",
            )}
          </Panel>
        )}
      </>
    );
  }
  if (page === "prs")
    return (
      <Pulls
        state={s}
        project={project}
        subject={subject}
        locked={offlineLocked}
        canAttest={role !== "operator"}
        run={run}
      />
    );
  if (page === "chat") {
    return (
      <>
        <Panel title="Project manager">
          {!project && (
            <div className="panel-body">
              <Field title="Project">
                <select
                  value={selected?.id || ""}
                  onChange={(e) => setPicked(e.target.value)}
                >
                  {s.projects.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </select>
              </Field>
            </div>
          )}
          <div className="notice">
            <Icon name="chat" />
            <span>{s.capabilities.reason}</span>
          </div>
          {messages
            .filter((m) => m.projectId === selected?.id)
            .map((m) => (
              <div key={m.id} className="bubble">
                <small>You · {when(m.createdAt)}</small>
                <p>{m.body}</p>
                <Badge tone="warn">{human(m.status)}</Badge>{" "}
                <small>{m.reason}</small>
              </div>
            ))}
          <form
            className="panel-body"
            onSubmit={async (e) => {
              e.preventDefault();
              if (!selected || locked) return;
              const ok = await run(
                () =>
                  post("messages", {
                    projectId: selected.id,
                    body: chat,
                    requestId: chatKey.current,
                  }),
                "Message saved. Model authorization is pending.",
              );
              if (ok) {
                setChat("");
                chatKey.current = crypto.randomUUID();
              }
            }}
          >
            <Field title="Message to the project manager">
              <textarea
                maxLength={4000}
                required
                disabled={!selected}
                value={chat}
                onChange={(e) => {
                  setChat(e.target.value);
                  chatKey.current = crypto.randomUUID();
                }}
              />
            </Field>
            <button disabled={locked || !chat.trim() || !selected}>
              Save message
            </button>
          </form>
        </Panel>
      </>
    );
  }
  return (
    <>
      {!s.capabilities.demo && (!s.viewer || s.viewer.role === "owner") && (
        <ModelAccess key={s.viewer?.id || "owner"} />
      )}
      <Panel title="Repository connections">
        {projects.length
          ? projects.map((p) => (
              <div className="device" key={p.id}>
                <Row
                  icon="repo"
                  heading={p.name}
                  detail={p.repository}
                  tail={
                    <Badge tone={p.paused ? "warn" : "neutral"}>
                      {p.paused ? "Dispatch paused" : "Dispatch enabled"}
                    </Badge>
                  }
                />
                <div className="device-detail">
                  <span>
                    {p.paused
                      ? "New assignments are on hold."
                      : "Pause to prevent new assignments."}
                  </span>
                  <button
                    disabled={locked}
                    onClick={() =>
                      void run(
                        async () => {
                          const res = await post<{ decisionId?: string }>(
                            "projects/" + enc(p.id) + "/dispatch",
                            { paused: !p.paused, version: p.version },
                          );
                          if (res.decisionId)
                            window.location.hash = link(
                              "decisions",
                              p.id,
                              res.decisionId,
                            );
                        },
                        p.paused
                          ? "Resume request sent for a decision."
                          : "New dispatch paused.",
                      )
                    }
                  >
                    {p.paused ? "Request resume" : "Pause dispatch"}
                  </button>
                </div>
              </div>
            ))
          : note(
              "No projects registered",
              "Register a GitHub repository to configure a project.",
              "repo",
            )}
      </Panel>
      <DispatchControl workspace={s.workspace} run={run} locked={locked} />
      <Settings state={s} projects={projects} locked={locked} run={run} />
      {!project && role === "owner" && !s.capabilities.demo && (
        <Members locked={locked} run={run} projects={s.projects} />
      )}
    </>
  );
}
