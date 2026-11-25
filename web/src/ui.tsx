// SPDX-License-Identifier: MPL-2.0
import { useEffect, useRef, type ReactNode } from "react";

const paths = {
  overview: "M3 3h7v7H3z M14 3h7v7h-7z M3 14h7v7H3z M14 14h7v7h-7z",
  plans: "M4 5h16v15H4z M4 10h16 M8 3v4 M16 3v4 M8 14h4",
  work: "M5 3v18 M5 5h9a4 4 0 0 1 0 8H5 M5 18h14",
  prs: "M6 7v10 M18 17v-6a6 6 0 0 0-6-6 M15 2l-3 3 3 3 M8 5a2 2 0 1 1-4 0 2 2 0 0 1 4 0 M8 19a2 2 0 1 1-4 0 2 2 0 0 1 4 0 M20 19a2 2 0 1 1-4 0 2 2 0 0 1 4 0",
  incidents: "M13 2 3 14h8l-1 8 11-13h-9z",
  decisions: "M12 3 21 12l-9 9-9-9z M9 12l2 2 4-4",
  devices: "M3 4h18v13H3z M9 21h6 M12 17v4",
  chat: "M3 4h18v13H9l-6 4z M7 8h10 M7 12h6",
  settings: "M3 6h5m5 0h8 M3 12h10m5 0h3 M3 18h3m5 0h10 M8 3v6m5 0v6m-7 0v6",
  arrow: "M5 12h14m-5-5 5 5-5 5",
  plus: "M5 12h14 M12 5v14",
  close: "m6 6 12 12 M18 6 6 18",
  activity: "M2 12h5l3-8 4 16 3-8h5",
  repo: "M4 5a2 2 0 0 1 2-2h13v18H6a2 2 0 0 1-2-2z M4 18q0-2 3-2h12 M8 7h6",
  search: "M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0 M15 15l6 6",
  menu: "M3 6h18 M3 12h18 M3 18h18",
  back: "m14 5-7 7 7 7",
  theme: "M21 12a9 9 0 1 1-9-9 7 7 0 0 0 9 9",
  clock: "M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0 M12 6v6l4 2",
  signout: "M10 3H4v18h6 M10 12h11m-5-5 5 5-5 5",
  check: "m4 12 5 5L20 6",
  refresh: "M20 7a9 9 0 1 0 1 8 M20 2v6h-6",
};
export type IconName = keyof typeof paths;
export function Icon({ name, size = 18 }: { name: IconName; size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.65"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={paths[name]} />
    </svg>
  );
}
export function Brand() {
  return (
    <span className="brand">
      <img src="/brand/bugsnitch-logo.svg" width={28} height={25} alt="" />
      Bugsnitch
    </span>
  );
}
export function Empty({
  icon,
  title,
  children,
}: {
  icon: IconName;
  title: string;
  children?: ReactNode;
}) {
  return (
    <div className="empty">
      <span className="empty-icon">
        <Icon name={icon} size={23} />
      </span>
      <h3>{title}</h3>
      {children && <p>{children}</p>}
    </div>
  );
}
export function Panel({
  title,
  action,
  children,
}: {
  title: ReactNode;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="panel">
      <div className="panel-head">
        <h2>{title}</h2>
        {action}
      </div>
      {children}
    </section>
  );
}
export function Badge({
  children,
  tone = "neutral",
}: {
  children: ReactNode;
  tone?: string;
}) {
  return <span className={"badge " + tone}>{children}</span>;
}
export function Modal({
  title,
  close,
  children,
}: {
  title: string;
  close: () => void;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current!;
    d.showModal();
    return () => d.close();
  }, []);
  return (
    <dialog
      ref={ref}
      onCancel={(e) => {
        e.preventDefault();
        close();
      }}
      aria-labelledby="dialogtitle"
    >
      <div className="modal-head">
        <h2 id="dialogtitle">{title}</h2>
        <button
          type="button"
          className="icon"
          aria-label="Close dialog"
          onClick={close}
        >
          <Icon name="close" />
        </button>
      </div>
      {children}
    </dialog>
  );
}
export function Field({
  title,
  children,
}: {
  title: string;
  children: ReactNode;
}) {
  return (
    <label className="field">
      <span>{title}</span>
      {children}
    </label>
  );
}
export const human = (s: string) =>
  s.replace(/[_.-]+/g, " ").replace(/^./, (c) => c.toUpperCase());
export function when(s: string | null | undefined) {
  if (!s) return "Unknown";
  const d = new Date(s);
  return isNaN(+d)
    ? "Unknown"
    : new Intl.DateTimeFormat(undefined, {
        month: "short",
        day: "numeric",
        hour: "numeric",
        minute: "2-digit",
      }).format(d);
}
export type Page =
  | "overview"
  | "plans"
  | "work"
  | "prs"
  | "incidents"
  | "decisions"
  | "devices"
  | "chat"
  | "settings";
export const pages: Record<Page, string> = {
  overview: "Overview",
  plans: "Plans",
  work: "Work",
  prs: "Pull requests",
  incidents: "Incidents",
  decisions: "Decisions",
  devices: "Devices",
  chat: "Project chat",
  settings: "Settings",
};
export function link(page: Page, project = "", subject = "") {
  const params = new URLSearchParams();
  if (project) params.set("project", project);
  if (subject) params.set("id", subject);
  return "#/" + page + (params.size ? "?" + params : "");
}
export function readRoute() {
  const [path, search] = window.location.hash.slice(2).split("?");
  const params = new URLSearchParams(search);
  return {
    page: (Object.hasOwn(pages, path || "") ? path : "overview") as Page,
    project: params.get("project") || "",
    subject: params.get("id") || "",
  };
}
