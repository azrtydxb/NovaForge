import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { scopedRepos, useWorkspace } from "../lib/workspace";
import {
  Async,
  Empty,
  Loading,
  Page,
  Panel,
  PanelHead,
} from "../components/ui";
import type { Agent, Dashboard } from "../lib/types";

/** Home is the redesign's desk: what needs this person, what the agents are
 * doing, what happened, and the repositories they touch. Every number is the
 * platform's own — where the platform has no number, the section says so
 * rather than rendering a plausible zero. */

const hour = (d: Date) => d.getHours();

export function Home() {
  const w = useWorkspace();

  const dash = useQuery({
    queryKey: ["dashboard", w.org],
    queryFn: () => api.get<Dashboard>(`/api/v1/orgs/${enc(w.org!)}/dashboard`),
    enabled: w.org !== null,
  });

  const agents = useQuery({
    queryKey: ["agents", w.org],
    queryFn: () =>
      api.get<{ agents: Agent[] }>(`/api/v1/orgs/${enc(w.org!)}/agents`),
    enabled: w.org !== null,
    refetchInterval: 5000,
  });

  const busy = (agents.data?.agents ?? []).filter(
    (a) => a.current_run_id !== "",
  );

  // The greeting follows the clock of the person looking at it, which is the
  // only clock that is correct for a greeting.
  const greeting =
    hour(new Date()) < 12
      ? "Good morning"
      : hour(new Date()) < 18
        ? "Good afternoon"
        : "Good evening";

  const needsYou = useMemo(() => {
    const d = dash.data;
    if (!d) return [];
    return d.exceptions;
  }, [dash.data]);

  return (
    <Page title="Home">
      <Async query={dash}>
        {(d) => {
          const running = d.agents_running;
          const summary = [
            needsYou.length > 0
              ? `${needsYou.length} ${
                  needsYou.length === 1 ? "thing needs" : "things need"
                } you`
              : null,
            running > 0 ? `${running} agents working` : null,
          ].filter(Boolean);
          return (
            <div style={{ marginBottom: 18 }}>
              <h1
                style={{
                  font: "600 24px var(--sans)",
                  margin: "0 0 4px",
                  color: "var(--fg)",
                }}
              >
                {greeting}, {me()}
              </h1>
              <p
                style={{
                  margin: 0,
                  font: "13px var(--sans)",
                  color: "var(--fg-muted)",
                }}
              >
                {summary.length > 0
                  ? summary.join(" · ")
                  : "Nothing needs you right now."}
              </p>
            </div>
          );
        }}
      </Async>

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "1.1fr 1fr",
          gap: 14,
          alignItems: "start",
        }}
      >
        <div style={{ display: "grid", gap: 14 }}>
          <Panel>
            <PanelHead title="Needs you" count={needsYou.length}>
              <Link to="/inbox">Open inbox</Link>
            </PanelHead>
            {needsYou.length === 0 ? (
              <Empty>Nothing needs you.</Empty>
            ) : (
              needsYou.map((e, i) => <ExceptionRow key={i} e={e} />)
            )}
          </Panel>

          <Panel>
            <PanelHead title="Activity">
              <span
                title="This deployment records agent activity; the human and
                release feeds are not published on any stream yet."
                style={{ font: "11px var(--sans)", color: "var(--fg-faint)" }}
              >
                agent activity
              </span>
            </PanelHead>
            <AgentActivity />
          </Panel>
        </div>

        <div style={{ display: "grid", gap: 14 }}>
          <Panel>
            <PanelHead title="Agents at work" count={busy.length}>
              <Link to="/agents">All agents</Link>
            </PanelHead>
            {busy.length === 0 ? (
              <Empty>No agents are running.</Empty>
            ) : (
              busy.map((a) => <AgentRow key={a.id} a={a} />)
            )}
          </Panel>

          <Panel>
            <PanelHead title="Repositories">
              <Link to="/repos">View all</Link>
            </PanelHead>
            <RepoQuickList />
          </Panel>
        </div>
      </div>
    </Page>
  );
}

/** me reads the signed-in username from the session the app already holds, so
 * the greeting greets the person the header shows. */
function me(): string {
  try {
    const raw = localStorage.getItem("nf-user");
    return raw ? JSON.parse(raw) : "there";
  } catch {
    return "there";
  }
}

const REASON_TONE: Record<string, { label: string; color: string }> = {
  approval: { label: "APPROVE", color: "var(--warn)" },
  review: { label: "REVIEW", color: "var(--info)" },
  question: { label: "QUESTION", color: "var(--violet)" },
  gate: { label: "GATE FAILED", color: "var(--bad)" },
  maintenance: { label: "PROPOSAL", color: "var(--fg-muted)" },
};

function ExceptionRow({
  e,
}: {
  e: { key: string; title: string; reason: string };
}) {
  const tone = REASON_TONE[e.reason] ?? {
    label: e.reason.toUpperCase(),
    color: "var(--fg-muted)",
  };
  const to = e.reason.startsWith("gate")
    ? "/ci"
    : e.key.includes("-")
      ? `/work/${e.key.split("/")[0] ?? ""}/${e.key}`
      : "/runs";
  return (
    <div
      style={{
        display: "flex",
        alignItems: "baseline",
        gap: 10,
        padding: "8px 0",
        borderBottom: "1px solid var(--line)",
      }}
    >
      <span
        style={{
          flex: "none",
          font: "600 9px var(--mono)",
          letterSpacing: ".08em",
          color: tone.color,
          width: 92,
        }}
      >
        {tone.label}
      </span>
      <span style={{ flex: 1, font: "13px var(--sans)" }}>
        <Link to={to} style={{ color: "var(--fg)" }}>
          {e.title}
        </Link>
      </span>
      <Link to={to} style={{ font: "12px var(--sans)", flex: "none" }}>
        Open
      </Link>
    </div>
  );
}

function AgentRow({ a }: { a: Agent }) {
  const since = a.busy_since ? timeAgo(a.busy_since) : "";
  return (
    <Link
      to={`/agent-runs/${a.current_run_id}`}
      style={{
        display: "flex",
        alignItems: "center",
        gap: 10,
        padding: "8px 0",
        borderBottom: "1px solid var(--line)",
        color: "var(--fg)",
      }}
    >
      <span
        style={{
          width: 26,
          height: 26,
          borderRadius: 99,
          background: "#2f3542",
          display: "grid",
          placeItems: "center",
          font: "600 11px var(--sans)",
          color: "#fff",
          flex: "none",
        }}
      >
        {a.name.slice(0, 1).toUpperCase()}
      </span>
      <span style={{ flex: 1, minWidth: 0 }}>
        <span style={{ display: "flex", gap: 8, alignItems: "baseline" }}>
          <b style={{ font: "600 13px var(--sans)" }}>{a.name}</b>
          {a.current_work_item_key ? (
            <span
              style={{ font: "11px var(--mono)", color: "var(--fg-muted)" }}
            >
              {a.current_work_item_key}
            </span>
          ) : null}
          <span
            style={{
              marginLeft: "auto",
              font: "11px var(--sans)",
              color: "var(--fg-faint)",
            }}
          >
            {since}
          </span>
        </span>
        <span
          style={{
            display: "block",
            font: "12px var(--sans)",
            color: "var(--fg-muted)",
            whiteSpace: "nowrap",
            overflow: "hidden",
            textOverflow: "ellipsis",
          }}
        >
          {a.role}
        </span>
      </span>
    </Link>
  );
}

/** AgentActivity is the platform's own record of what its agents did — the
 * same list the Agents screen reads. The design also draws human and release
 * events here; those are not published on any stream in this deployment, so
 * the panel says which feed it is showing instead of inventing rows. */
function AgentActivity() {
  const w = useWorkspace();
  const repos = scopedRepos(w);
  const q = useQuery({
    queryKey: ["agent-activity", w.org],
    queryFn: () =>
      api.get<{ agents: Agent[] }>(`/api/v1/orgs/${enc(w.org!)}/agents`),
    enabled: w.org !== null,
    refetchInterval: 10_000,
  });
  const items = (q.data?.agents ?? []).filter((a) => a.busy_since);
  if (items.length === 0) return <Empty>No agent activity recorded yet.</Empty>;
  return (
    <div>
      {items.slice(0, 6).map((a) => (
        <div
          key={a.id}
          style={{
            display: "flex",
            gap: 8,
            alignItems: "baseline",
            padding: "7px 0",
            borderBottom: "1px solid var(--line)",
            font: "13px var(--sans)",
          }}
        >
          <b>{a.name}</b>
          <span style={{ color: "var(--fg-muted)" }}>
            {a.current_work_item_key
              ? `working ${a.current_work_item_key}`
              : "working"}
          </span>
          <span
            style={{
              marginLeft: "auto",
              font: "11px var(--sans)",
              color: "var(--fg-faint)",
            }}
          >
            {timeAgo(a.busy_since)}
          </span>
        </div>
      ))}
      <div
        style={{
          font: "11px var(--sans)",
          color: "var(--fg-faint)",
          padding: "8px 0 0",
        }}
      >
        across {repos.length}{" "}
        {repos.length === 1 ? "repository" : "repositories"} · human and release
        events are not published in this deployment
      </div>
    </div>
  );
}

function RepoQuickList() {
  const w = useWorkspace();
  const repos = scopedRepos(w);
  const all = useQuery({
    queryKey: ["repos", w.org],
    queryFn: () =>
      api.get<{ repos: { name: string }[] }>(
        `/api/v1/orgs/${enc(w.org!)}/repos`,
      ),
    enabled: w.org !== null,
  });
  const list = all.data?.repos ?? repos;
  const [find, setFind] = useState("");
  return (
    <div>
      <input
        value={find}
        onChange={(e) => setFind(e.target.value)}
        placeholder="Find a repository"
        style={{
          width: "100%",
          background: "var(--panel-2)",
          border: "1px solid var(--line)",
          borderRadius: 7,
          padding: "6px 10px",
          font: "12px var(--sans)",
          color: "var(--fg)",
          marginBottom: 6,
        }}
      />
      {list
        .filter((r) => r.name.toLowerCase().includes(find.toLowerCase()))
        .slice(0, 8)
        .map((r) => (
          <Link
            key={r.name}
            to={`/repos/${r.name}`}
            style={{
              display: "flex",
              gap: 8,
              alignItems: "center",
              padding: "6px 2px",
              font: "13px var(--sans)",
              color: "var(--fg)",
              borderBottom: "1px solid var(--line)",
            }}
          >
            ◆ {r.name}
          </Link>
        ))}
      {all.isLoading ? <Loading /> : null}
    </div>
  );
}

export function timeAgo(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  if (Number.isNaN(ms)) return "";
  const m = Math.floor(ms / 60_000);
  if (m < 1) return "now";
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}

/** Row is the list row Runs and Work build their tables on. It lives here
 * because the old Home and the redesign's desk share it. */
export function Row({ children }: { children: React.ReactNode }) {
  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        gap: 11,
        padding: "9px 14px",
        borderBottom: "1px solid var(--line)",
      }}
    >
      {children}
    </div>
  );
}
