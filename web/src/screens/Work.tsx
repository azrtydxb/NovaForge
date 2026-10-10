import { useState } from "react";
import { Link } from "react-router-dom";
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { scopedRepos, useWorkspace } from "../lib/workspace";
import {
  Empty,
  Failed,
  Loading,
  Page,
  Panel,
  PanelHead,
  StatePill,
} from "../components/ui";
import { Row } from "./Home";
import { Dialog, NewButton } from "../components/Dialog";
import type { Agent, WorkItem } from "../lib/types";

/** TYPES is the closed set the work schema allows. Offering anything else
 * would be offering an item the platform will refuse to create. */
export const TYPES = [
  "feature",
  "bug",
  "refactor",
  "security",
  "tech_debt",
  "research",
  "architecture",
  "upgrade",
  "incident",
  "documentation",
];

/** FILTERS are the design's status filters, mapped onto the states the work
 * schema actually allows (work/migrations/000001: open, planning, in_progress,
 * review, done, blocked). "All" is not a state, it is the absence of a filter.
 * The board's columns are the same six states, so a filter and a column always
 * mean the same thing. */
const FILTERS: { label: string; state: string | null }[] = [
  { label: "All", state: null },
  { label: "Working", state: "in_progress" },
  { label: "Review", state: "review" },
  { label: "Blocked", state: "blocked" },
  { label: "Planning", state: "planning" },
  { label: "Open", state: "open" },
  { label: "Done", state: "done" },
];

/** relTime renders the design's quiet relative ages ("2h", "3d"). Timestamps
 * are absolute in the data; a wall-clock age is presentation, so it is
 * computed from the viewer's clock at render, never stored. */
export function relTime(iso: string): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "";
  const s = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (s < 60) return "now";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  const d = Math.floor(h / 24);
  if (d < 30) return `${d}d`;
  return `${Math.floor(d / 30)}mo`;
}

/** lines turns a one-per-line textarea into the list the API takes. */
function lines(v: string | undefined): string[] {
  return (v ?? "")
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);
}

type View = "list" | "board";

export function Work() {
  const w = useWorkspace();
  const repos = scopedRepos(w);
  const [filter, setFilter] = useState<string | null>(null);
  const [view, setView] = useState<View>("list");
  const [creating, setCreating] = useState(false);
  const [starting, setStarting] = useState<{
    repo: string;
    key: string;
  } | null>(null);
  // The board's one honest message: why a dropped card did not move. Cleared
  // by the next drop so it never describes a move that already happened.
  const [boardNote, setBoardNote] = useState<string | null>(null);
  const qc = useQueryClient();

  // Agents are listed so a run can be started against a named one: the
  // platform requires an agent id, and asking a person to paste a uuid would
  // be asking them to do the lookup the application can do.
  const agents = useQuery({
    queryKey: ["agents", w.org],
    queryFn: () =>
      api.get<{ agents: Agent[] }>(`/api/v1/orgs/${enc(w.org!)}/agents`),
    enabled: w.org !== null,
  });
  const enabledAgents = (agents.data?.agents ?? []).filter((a) => a.enabled);

  const startRun = useMutation({
    mutationFn: (v: Record<string, string>) => {
      const agent = enabledAgents.find((a) => a.name === v.agent);
      if (!agent) throw new Error("pick an agent");
      return api.post(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(starting!.repo)}/agent-runs`,
        { agent_id: agent.id, work_item_key: starting!.key },
      );
    },
    onSuccess: () => {
      setStarting(null);
      qc.invalidateQueries();
    },
  });

  const create = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post(`/api/v1/orgs/${enc(w.org!)}/repos/${enc(v.repo!)}/work`, {
        type: v.type,
        goal: v.goal,
        acceptance: lines(v.acceptance),
        constraints: lines(v.constraints),
        required_gates: lines(v.required_gates),
      }),
    onSuccess: () => {
      setCreating(false);
      qc.invalidateQueries();
    },
  });

  // The only state change the list endpoint's data can back. The design's
  // board tempts a drag between any two columns, but the transitions RPC is
  // deliberately narrow — block or reopen eligible human work — and a board
  // that moved cards it cannot persist would be drawing a fiction.
  const move = useMutation({
    mutationFn: (v: { repo: string; key: string; from: string; to: string }) =>
      api.post(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(v.repo)}/work/${enc(v.key)}/transitions`,
        { expected_state: v.from, to_state: v.to },
      ),
    onSuccess: () => qc.invalidateQueries(),
  });

  const queries = useQueries({
    queries: repos.map((r) => ({
      queryKey: ["work", w.org, r.name],
      queryFn: () =>
        api.get<{ items: WorkItem[] }>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(r.name)}/work`,
        ),
      enabled: w.org !== null,
    })),
  });

  const loading = queries.some((q) => q.isLoading);
  const listError = queries.find((q) => q.error)?.error;
  const all = queries
    .flatMap((q, i) =>
      (q.data?.items ?? []).map((item) => ({ item, repo: repos[i]!.name })),
    )
    .sort((a, b) => b.item.created_at.localeCompare(a.item.created_at));
  const rows = all.filter((x) => filter === null || x.item.state === filter);

  /** drop applies a drag on the board. Anything the transitions RPC does not
   * offer is answered with a sentence, never with silence — silence is
   * indistinguishable from a board that worked and did nothing. */
  const drop = (target: { item: WorkItem; repo: string }, to: string) => {
    const { item, repo } = target;
    if (item.state === to) return;
    if (
      (to === "open" || to === "blocked") &&
      (item.state === "open" || item.state === "blocked")
    ) {
      setBoardNote(null);
      move.mutate({ repo, key: item.key, from: item.state, to });
      return;
    }
    setBoardNote(
      `${item.key} stays ${item.state.replace(/_/g, " ")}: a person can only block or reopen work. ` +
        `The other moves — planning, working, review, done — are made by the platform's execution, ` +
        `so the board does not pretend to make them.`,
    );
  };

  return (
    <Page
      title="Work"
      subtitle="Typed engineering intent: goal, acceptance criteria, constraints, required gates"
      actions={
        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          <div
            role="tablist"
            aria-label="Work view"
            style={{
              display: "flex",
              border: "1px solid var(--line-2)",
              borderRadius: 8,
              overflow: "hidden",
            }}
          >
            {(["list", "board"] as View[]).map((v) => (
              <button
                key={v}
                role="tab"
                aria-selected={view === v}
                onClick={() => setView(v)}
                style={{
                  padding: "7px 13px",
                  border: "none",
                  background: view === v ? "var(--accent-soft)" : "transparent",
                  color: view === v ? "var(--fg)" : "var(--fg-muted)",
                  font: "500 12px var(--sans)",
                  cursor: "pointer",
                }}
              >
                {v === "list" ? "List" : "Board"}
              </button>
            ))}
          </div>
          {repos.length > 0 ? (
            <NewButton
              label="New Work Item"
              onClick={() => setCreating(true)}
            />
          ) : null}
        </div>
      }
    >
      {starting ? (
        <Dialog
          title={`Start an Agent Run on ${starting.key}`}
          submitLabel="Start"
          fields={[
            {
              name: "agent",
              label: "Agent",
              type: "select",
              options: enabledAgents.map((a) => a.name),
              required: true,
              help: "The run is sponsored by you: an agent always has a human answerable for it.",
            },
          ]}
          busy={startRun.isPending}
          error={startRun.error}
          onSubmit={(v) => startRun.mutate(v)}
          onClose={() => setStarting(null)}
        />
      ) : null}
      {creating ? (
        <Dialog
          title="New Work Item"
          submitLabel="Create"
          fields={[
            {
              name: "repo",
              label: "Repository",
              type: "select",
              options: repos.map((r) => r.name),
              required: true,
            },
            {
              name: "type",
              label: "Type",
              type: "select",
              options: TYPES,
              required: true,
            },
            {
              name: "goal",
              label: "Goal",
              required: true,
              placeholder: "Add a README describing this service",
            },
            {
              name: "acceptance",
              label: "Acceptance criteria",
              type: "textarea",
              placeholder: "One per line",
              help: "What must be true when this is done. An agent is judged against these.",
            },
            {
              name: "constraints",
              label: "Constraints",
              type: "textarea",
              placeholder: "One per line",
              help: "What the work must not do, e.g. no new datastore.",
            },
            {
              name: "required_gates",
              label: "Required gates",
              type: "textarea",
              placeholder: "One per line",
              help: "Gates the change must pass before it merges: tests, architecture, security, api-compatibility, dependencies, quality, documentation.",
            },
          ]}
          busy={create.isPending}
          error={create.error}
          onSubmit={(v) => create.mutate(v)}
          onClose={() => setCreating(false)}
        />
      ) : null}

      <div
        style={{ display: "flex", gap: 6, marginBottom: 12, flexWrap: "wrap" }}
      >
        {FILTERS.map((f) => {
          const active = filter === f.state;
          const n =
            f.state === null
              ? all.length
              : all.filter((x) => x.item.state === f.state).length;
          return (
            <button
              key={f.label}
              onClick={() => setFilter(f.state)}
              style={{
                padding: "5px 11px",
                borderRadius: 7,
                border: "1px solid var(--line)",
                background: active ? "var(--accent-soft)" : "transparent",
                color: active ? "var(--fg)" : "var(--fg-muted)",
                font: "500 12px var(--sans)",
                cursor: "pointer",
              }}
            >
              {f.label}
              <span
                style={{
                  font: "600 10px var(--mono)",
                  color: active ? "var(--link)" : "var(--fg-faint)",
                  marginLeft: 6,
                }}
              >
                {n}
              </span>
            </button>
          );
        })}
      </div>

      {move.error ? (
        <div style={{ marginBottom: 12 }}>
          <Failed error={move.error} />
        </div>
      ) : null}

      {view === "list" ? (
        <Panel>
          <PanelHead>
            <span style={{ width: 80 }}>KEY</span>
            <span style={{ flex: 1 }}>GOAL</span>
            <span style={{ width: 90 }}>STATE</span>
            <span style={{ width: 74 }} />
          </PanelHead>
          {loading ? (
            <Loading />
          ) : listError ? (
            <Failed error={listError} />
          ) : rows.length === 0 ? (
            <Empty>
              {filter === null
                ? "No Work Items in this scope yet."
                : `No Work Items are ${filter.replace(/_/g, " ")}.`}
            </Empty>
          ) : (
            rows.map(({ item, repo }) => (
              <Row key={item.id}>
                <Link
                  to={`/work/${enc(repo)}/${enc(item.key)}`}
                  style={{ width: 80, font: "600 12px var(--mono)" }}
                >
                  {item.key}
                </Link>
                <span style={{ flex: 1, minWidth: 0 }}>
                  <span
                    style={{
                      display: "block",
                      font: "13px var(--sans)",
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                      whiteSpace: "nowrap",
                    }}
                  >
                    {item.goal}
                  </span>
                  {/* The design's quiet metadata line: everything about the
                      row that is identity rather than content, in one mono
                      whisper instead of four columns of its own. */}
                  <span
                    style={{
                      display: "block",
                      marginTop: 2,
                      font: "11px var(--mono)",
                      color: "var(--fg-faint)",
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                      whiteSpace: "nowrap",
                    }}
                  >
                    {item.type} · {repo} ·{" "}
                    {item.assignee_id
                      ? `assigned to ${item.assignee_kind}`
                      : "unassigned"}{" "}
                    · {relTime(item.created_at)}
                  </span>
                </span>
                <span style={{ width: 90 }}>
                  <StatePill state={item.state} />
                </span>
                <span style={{ width: 74, textAlign: "right" }}>
                  {item.state === "open" && enabledAgents.length > 0 ? (
                    <button
                      onClick={() => setStarting({ repo, key: item.key })}
                      style={{
                        padding: "4px 10px",
                        border: "1px solid var(--line-2)",
                        borderRadius: 7,
                        background: "transparent",
                        color: "var(--link)",
                        font: "11px var(--sans)",
                        cursor: "pointer",
                      }}
                    >
                      Start
                    </button>
                  ) : null}
                </span>
              </Row>
            ))
          )}
        </Panel>
      ) : (
        <>
          {boardNote ? (
            <div
              style={{
                marginBottom: 12,
                display: "flex",
                alignItems: "baseline",
                gap: 10,
                padding: "9px 13px",
                border: "1px solid var(--line-2)",
                borderRadius: 9,
                font: "12px var(--sans)",
                color: "var(--fg-muted)",
                lineHeight: 1.5,
              }}
            >
              <span style={{ flex: 1 }}>{boardNote}</span>
              <button
                onClick={() => setBoardNote(null)}
                aria-label="Dismiss note"
                style={{
                  border: "none",
                  background: "transparent",
                  color: "var(--fg-faint)",
                  font: "12px var(--sans)",
                  cursor: "pointer",
                }}
              >
                dismiss
              </button>
            </div>
          ) : null}
          {loading ? (
            <Panel>
              <Loading />
            </Panel>
          ) : listError ? (
            <Panel>
              <Failed error={listError} />
            </Panel>
          ) : (
            // One list fetched, six client-filtered columns: the board draws
            // no query of its own, so a card on it is always a Work Item the
            // platform actually returned.
            <div style={{ overflowX: "auto", paddingBottom: 4 }}>
              <div
                style={{
                  display: "grid",
                  gridTemplateColumns: "repeat(6, minmax(196px, 1fr))",
                  gap: 10,
                  minWidth: 1180,
                  alignItems: "start",
                }}
              >
                {FILTERS.filter((f) => f.state !== null).map((col) => {
                  const state = col.state!;
                  const cards = rows.filter((x) => x.item.state === state);
                  return (
                    <BoardColumn
                      key={state}
                      label={col.label}
                      cards={cards}
                      onDrop={(item) => item && drop(item, state)}
                    />
                  );
                })}
              </div>
            </div>
          )}
        </>
      )}
    </Page>
  );
}

/** BoardColumn is one state's lane. It accepts a dragged card anywhere, and
 * decides with the caller whether the drop means a transition the transitions
 * RPC offers, or a sentence about why not — an empty lane that swallowed a
 * drop would look exactly like a lane that worked. */
function BoardColumn({
  label,
  cards,
  onDrop,
}: {
  label: string;
  cards: { item: WorkItem; repo: string }[];
  onDrop: (item: { item: WorkItem; repo: string } | null) => void;
}) {
  const [hover, setHover] = useState(false);
  return (
    <Panel
      style={{
        background: hover ? "var(--panel-2)" : "var(--panel)",
        borderColor: hover ? "var(--line-3)" : "var(--line)",
      }}
    >
      <PanelHead title={label} count={cards.length} />
      <div
        onDragOver={(e) => {
          e.preventDefault();
          setHover(true);
        }}
        onDragLeave={() => setHover(false)}
        onDrop={(e) => {
          e.preventDefault();
          setHover(false);
          // A malformed payload is dropped, not crashed on: the drag carries
          // only what this page put on it, but a foreign drop should land as
          // a no-op rather than an exception.
          try {
            const raw = e.dataTransfer.getData("text/nf-work");
            const parsed = raw
              ? (JSON.parse(raw) as { item: WorkItem; repo: string })
              : null;
            onDrop(parsed);
          } catch {
            onDrop(null);
          }
        }}
        style={{ minHeight: 72 }}
      >
        {cards.length === 0 ? (
          <div
            style={{
              padding: "14px 12px",
              font: "11px var(--mono)",
              color: "var(--fg-faint)",
            }}
          >
            {hover ? "drop to move here" : "—"}
          </div>
        ) : (
          cards.map(({ item, repo }) => (
            <div
              key={item.id}
              draggable={
                item.state === "open" || item.state === "blocked"
                  ? item.assignee_kind !== "agent"
                  : false
              }
              onDragStart={(e) =>
                e.dataTransfer.setData(
                  "text/nf-work",
                  JSON.stringify({ item, repo }),
                )
              }
              style={{
                margin: 8,
                padding: "9px 11px",
                border: "1px solid var(--line)",
                borderRadius: 8,
                background: "var(--panel)",
                cursor:
                  item.state === "open" || item.state === "blocked"
                    ? item.assignee_kind !== "agent"
                      ? "grab"
                      : "default"
                    : "default",
              }}
            >
              <div
                style={{
                  display: "flex",
                  alignItems: "baseline",
                  justifyContent: "space-between",
                  gap: 8,
                }}
              >
                <Link
                  to={`/work/${enc(repo)}/${enc(item.key)}`}
                  style={{ font: "600 11px var(--mono)" }}
                >
                  {item.key}
                </Link>
                <span
                  style={{ font: "10px var(--mono)", color: "var(--fg-faint)" }}
                >
                  {relTime(item.created_at)}
                </span>
              </div>
              <div
                style={{
                  marginTop: 4,
                  font: "12px/1.5 var(--sans)",
                  color: "var(--fg-dim)",
                  display: "-webkit-box",
                  WebkitLineClamp: 3,
                  WebkitBoxOrient: "vertical",
                  overflow: "hidden",
                }}
              >
                {item.goal}
              </div>
              <div
                style={{
                  marginTop: 6,
                  font: "10px var(--mono)",
                  color: "var(--fg-faint)",
                  overflow: "hidden",
                  textOverflow: "ellipsis",
                  whiteSpace: "nowrap",
                }}
              >
                {item.type} · {repo}
              </div>
            </div>
          ))
        )}
      </div>
    </Panel>
  );
}
