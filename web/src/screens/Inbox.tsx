import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import { Async, Empty, Page, Panel } from "../components/ui";

/** Inbox is the design's unified Inbox: everything the platform observed
 * that needs this person, in one list, with the platform's own reasons.
 *
 * The REASON filter list is the design's, held statically: a reason no
 * publisher on this platform can produce simply never has rows, and shows as
 * an empty filter rather than being dropped from the list. */
export function Inbox() {
  const w = useWorkspace();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [tab, setTab] = useState<"inbox" | "saved" | "done">("inbox");
  const [reason, setReason] = useState<string>("all");
  const [repo, setRepo] = useState<string>("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [cursor, setCursor] = useState(0);

  const list = useQuery({
    queryKey: ["inbox", w.org, tab],
    queryFn: () =>
      api.get<{ notifications: Notif[] }>(
        `/api/v1/orgs/${enc(w.org!)}/inbox?state=${tab}`,
      ),
    enabled: w.org !== null,
  });

  const notifications = list.data?.notifications ?? [];

  // The counts beside each filter are computed from the tab's own list —
  // the platform's rows, not a second opinion about them.
  const rows = useMemo(() => {
    return notifications.filter(
      (n) =>
        (reason === "all" || n.reason === reason) &&
        (repo === "" || n.repo === repo),
    );
  }, [notifications, reason, repo]);

  const unread = useMemo(
    () => notifications.filter((n) => n.state === "inbox").length,
    [notifications],
  );

  function invalidate() {
    void qc.invalidateQueries({ queryKey: ["inbox", w.org] });
    void qc.invalidateQueries({ queryKey: ["inbox-unread"] });
  }

  const act = useMutation({
    mutationFn: async (v: {
      id: string;
      op: string;
      until?: string;
      saved?: boolean;
    }) => {
      if (v.op === "done")
        await api.post(`/api/v1/orgs/${enc(w.org!)}/inbox/${enc(v.id)}/done`);
      else if (v.op === "snooze")
        await api.post(
          `/api/v1/orgs/${enc(w.org!)}/inbox/${enc(v.id)}/snooze`,
          {
            until: v.until,
          },
        );
      else
        await api.post(`/api/v1/orgs/${enc(w.org!)}/inbox/${enc(v.id)}/save`, {
          saved: v.saved,
        });
    },
    onSuccess: invalidate,
  });

  // Keep the cursor and the selection inside the list that exists.
  useEffect(() => {
    if (cursor >= rows.length) setCursor(0);
  }, [rows.length, cursor]);
  useEffect(() => {
    setSelected(new Set());
  }, [tab, reason, repo, w.org]);

  // j/k move, e done, s snooze, Enter opens. While a text field has focus the
  // keys are letters, not commands.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA")) return;
      if (rows.length === 0) return;
      if (e.key === "j") setCursor((c) => Math.min(c + 1, rows.length - 1));
      else if (e.key === "k") setCursor((c) => Math.max(c - 1, 0));
      else if (e.key === "e" && rows[cursor])
        act.mutate({ id: rows[cursor].id, op: "done" });
      else if (e.key === "s" && rows[cursor])
        act.mutate({
          id: rows[cursor].id,
          op: "snooze",
          until: new Date(Date.now() + 24 * 3600 * 1000).toISOString(),
        });
      else if (e.key === "Enter" && rows[cursor]?.link)
        navigate(rows[cursor].link);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [rows, cursor, act, navigate]);

  const preview = rows[Math.min(cursor, Math.max(rows.length - 1, 0))];

  return (
    <Page
      title="Inbox"
      subtitle="Everything the platform observed that needs you, in one list"
    >
      <div style={{ display: "flex", gap: 16, alignItems: "flex-start" }}>
        <Panel style={{ width: 260, flex: "none" }}>
          <div
            style={{
              padding: "12px 14px",
              borderBottom: "1px solid var(--line)",
            }}
          >
            {(
              [
                ["inbox", "Inbox"],
                ["saved", "Saved"],
                ["done", "Done"],
              ] as const
            ).map(([key, label]) => (
              <button
                key={key}
                onClick={() => setTab(key)}
                style={{
                  display: "flex",
                  width: "100%",
                  alignItems: "center",
                  gap: 8,
                  padding: "6px 8px",
                  border: "none",
                  background:
                    tab === key ? "var(--accent-soft)" : "transparent",
                  borderRadius: 7,
                  font: "500 13px var(--sans)",
                  color: tab === key ? "var(--link)" : "var(--fg)",
                  cursor: "pointer",
                }}
              >
                <span style={{ flex: 1, textAlign: "left" }}>{label}</span>
                {key === "inbox" && unread > 0 ? (
                  <span
                    style={{
                      font: "600 11px var(--mono)",
                      color: "var(--fg-muted)",
                    }}
                  >
                    {unread}
                  </span>
                ) : null}
              </button>
            ))}
          </div>
          <div
            style={{
              padding: "12px 14px",
              borderBottom: "1px solid var(--line)",
            }}
          >
            <div style={groupLabel}>REASON</div>
            {(function () {
              const counts = new Map<string, number>();
              for (const n of notifications) {
                counts.set(n.reason, (counts.get(n.reason) ?? 0) + 1);
              }
              return REASONS.map(([key, label]) => (
                <FilterRow
                  key={key}
                  label={label}
                  count={
                    key === "all"
                      ? notifications.length
                      : (counts.get(key) ?? 0)
                  }
                  active={reason === key}
                  onClick={() => setReason(key)}
                />
              ));
            })()}
          </div>
          <div style={{ padding: "12px 14px" }}>
            <div style={groupLabel}>REPOSITORIES</div>
            {(function () {
              const counts = new Map<string, number>();
              for (const n of notifications) {
                counts.set(n.repo, (counts.get(n.repo) ?? 0) + 1);
              }
              const names = [...counts.keys()].sort();
              return (
                <>
                  <FilterRow
                    label="All repositories"
                    count={notifications.length}
                    active={repo === ""}
                    onClick={() => setRepo("")}
                  />
                  {names.map((name) => (
                    <FilterRow
                      key={name}
                      label={name}
                      count={counts.get(name) ?? 0}
                      active={repo === name}
                      onClick={() => setRepo(name)}
                    />
                  ))}
                </>
              );
            })()}
          </div>
        </Panel>
        <Panel style={{ flex: 1, minWidth: 0 }}>
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: 10,
              padding: "9px 14px",
              borderBottom: "1px solid var(--line)",
            }}
          >
            <input
              type="checkbox"
              aria-label="Select all"
              checked={rows.length > 0 && selected.size === rows.length}
              onChange={(e) =>
                setSelected(
                  e.target.checked ? new Set(rows.map((r) => r.id)) : new Set(),
                )
              }
            />
            <span style={{ flex: 1 }} />
            <button
              onClick={() =>
                rows.forEach((r) => {
                  if (r.state === "inbox") act.mutate({ id: r.id, op: "done" });
                })
              }
              disabled={
                selected.size === 0 && rows.every((r) => r.state !== "inbox")
              }
              style={ghostButton}
            >
              Mark all done
            </button>
          </div>
          <Async query={list} empty={<Empty>Nothing here.</Empty>}>
            {() => (
              <div>
                {rows.length === 0 ? (
                  <Empty>
                    {tab === "inbox"
                      ? "Your inbox is empty. Reviews, approvals and gate failures land here."
                      : tab === "saved"
                        ? "Nothing saved yet. Save a notification to keep it here."
                        : "Nothing is done yet."}
                  </Empty>
                ) : (
                  groupByDay(rows).map(([heading, group]) => (
                    <div key={heading}>
                      <div style={groupHeading}>{heading}</div>
                      {group.map((n) => (
                        <Row
                          key={n.id}
                          n={n}
                          selected={selected.has(n.id)}
                          cursor={rows[cursor]?.id === n.id}
                          onSelect={() =>
                            setSelected((s) => {
                              const next = new Set(s);
                              if (next.has(n.id)) next.delete(n.id);
                              else next.add(n.id);
                              return next;
                            })
                          }
                          onCursor={() => setCursor(rows.indexOf(n))}
                        />
                      ))}
                    </div>
                  ))
                )}
              </div>
            )}
          </Async>
        </Panel>
        <Panel style={{ width: 340, flex: "none", position: "sticky", top: 0 }}>
          {preview ? (
            <div style={{ padding: "14px 16px" }}>
              <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
                <ReasonBadge reason={preview.reason} />
                <span
                  style={{ font: "12px var(--mono)", color: "var(--fg-muted)" }}
                >
                  {preview.repo} {preview.ref}
                </span>
                <span style={{ flex: 1 }} />
                <button
                  onClick={() =>
                    act.mutate({
                      id: preview.id,
                      op: "snooze",
                      until: new Date(
                        Date.now() + 24 * 3600 * 1000,
                      ).toISOString(),
                    })
                  }
                  style={ghostButton}
                  title="Snooze until tomorrow (s)"
                >
                  Snooze
                </button>
                <button
                  onClick={() => act.mutate({ id: preview.id, op: "done" })}
                  style={ghostButton}
                  title="Mark done (e)"
                >
                  Done
                </button>
              </div>
              <h2
                style={{ font: "600 15px var(--sans)", margin: "12px 0 4px" }}
              >
                {preview.title}
              </h2>
              <div
                style={{ font: "12px var(--sans)", color: "var(--fg-muted)" }}
              >
                {[
                  preview.actor_name,
                  preview.actor_kind,
                  rel(preview.created_at),
                ]
                  .filter(Boolean)
                  .join(" · ")}
              </div>
              <p
                style={{
                  font: "13px var(--sans)",
                  lineHeight: 1.6,
                  whiteSpace: "pre-wrap",
                }}
              >
                {preview.body}
              </p>
              <div style={{ display: "flex", gap: 8, marginTop: 12 }}>
                {preview.link ? (
                  <Link to={preview.link} style={openButton}>
                    Open
                  </Link>
                ) : null}
                <button
                  onClick={() =>
                    act.mutate({
                      id: preview.id,
                      op: "save",
                      saved: preview.state !== "saved",
                    })
                  }
                  style={ghostButton}
                >
                  {preview.state === "saved" ? "Unsave" : "Save"}
                </button>
              </div>
              {preview.reason === "agent_question" ? (
                <p
                  style={{
                    font: "12px var(--sans)",
                    color: "var(--fg-muted)",
                    marginTop: 12,
                  }}
                >
                  This deployment has no agent-question flow yet, so there is
                  nothing to answer here.
                </p>
              ) : null}
            </div>
          ) : (
            <Empty>Select a notification to read it here.</Empty>
          )}
        </Panel>
      </div>
      <div style={keyboardFooter}>j / k move · e done · s snooze · ⏎ open</div>
    </Page>
  );
}

/** One notification as the design's row: checkbox, repo, ref, reason badge,
 * title, snippet, relative time, actor initials. */
function Row({
  n,
  selected,
  cursor,
  onSelect,
  onCursor,
}: {
  n: Notif;
  selected: boolean;
  cursor: boolean;
  onSelect: () => void;
  onCursor: () => void;
}) {
  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        gap: 10,
        padding: "10px 14px",
        borderBottom: "1px solid var(--line)",
        background: cursor ? "var(--accent-softer)" : "transparent",
        cursor: "pointer",
      }}
      onClick={onCursor}
    >
      <input
        type="checkbox"
        aria-label="Select"
        checked={selected}
        onClick={(e) => e.stopPropagation()}
        onChange={onSelect}
      />
      <span
        style={{
          font: "600 12px var(--mono)",
          color: "var(--fg-muted)",
          width: 90,
          flex: "none",
        }}
      >
        {n.repo}
      </span>
      <span
        style={{
          font: "12px var(--mono)",
          color: "var(--link)",
          width: 64,
          flex: "none",
        }}
      >
        {n.ref}
      </span>
      <ReasonBadge reason={n.reason} />
      <span style={{ flex: 1, minWidth: 0 }}>
        <div style={{ font: "13px var(--sans)" }}>{n.title}</div>
        {n.body ? (
          <div
            style={{
              font: "11px var(--mono)",
              color: "var(--fg-muted)",
              marginTop: 2,
              overflow: "hidden",
              textOverflow: "ellipsis",
              whiteSpace: "nowrap",
            }}
          >
            {n.body}
          </div>
        ) : null}
      </span>
      <span style={{ font: "11px var(--mono)", color: "var(--fg-faint)" }}>
        {rel(n.created_at)}
      </span>
      <Initials n={n} />
    </div>
  );
}

/** REASONS is the design's filter list, held statically and in the order the
 * artboard shows it. "review_requested" and the rest are the backend's own
 * reason strings; a reason with no publisher on this deployment simply has a
 * zero count and an empty list, which is the honest rendering. */
const REASONS: [string, string][] = [
  ["all", "All"],
  ["review_requested", "Review requested"],
  ["approval", "Approvals"],
  ["agent_question", "Agent questions"],
  ["mention", "Mentions"],
  ["gate_failure", "Gate failures"],
  ["maintenance", "Maintenance"],
];

const REASON_LABEL: Record<string, string> = Object.fromEntries(
  REASONS.filter(([k]) => k !== "all").map(([k, v]) => [
    k,
    v.replace(/s$/, ""),
  ]),
);

function ReasonBadge({ reason }: { reason: string }) {
  const [bg, fg] = REASON_TONE[reason] ?? [
    "rgba(139,145,160,.13)",
    "var(--fg-muted)",
  ];
  return (
    <span
      style={{
        background: bg,
        color: fg,
        borderRadius: 99,
        padding: "2px 8px",
        font: "600 10px var(--mono)",
        whiteSpace: "nowrap",
        flex: "none",
      }}
    >
      {(REASON_LABEL[reason] ?? reason).replace(/_/g, " ")}
    </span>
  );
}

const REASON_TONE: Record<string, [string, string]> = {
  review_requested: ["var(--info-bg)", "var(--link)"],
  approval: ["var(--warn-bg)", "var(--warn)"],
  gate_failure: ["var(--bad-bg)", "var(--bad)"],
  agent_question: ["var(--ok-bg)", "var(--ok)"],
  mention: ["var(--accent-soft)", "var(--link)"],
  maintenance: ["rgba(216,166,255,.16)", "var(--violet)"],
};

function FilterRow({
  label,
  count,
  active,
  onClick,
}: {
  label: string;
  count: number;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      style={{
        display: "flex",
        width: "100%",
        alignItems: "center",
        gap: 8,
        padding: "5px 8px",
        border: "none",
        borderRadius: 7,
        background: active ? "var(--accent-soft)" : "transparent",
        color: active ? "var(--link)" : "var(--fg-dim)",
        font: "500 12px var(--sans)",
        cursor: "pointer",
        textAlign: "left",
      }}
    >
      <span style={{ flex: 1 }}>{label}</span>
      {count > 0 ? (
        <span
          style={{ font: "600 11px var(--mono)", color: "var(--fg-muted)" }}
        >
          {count}
        </span>
      ) : null}
    </button>
  );
}

/** Initials renders the author column. The publishers know most actors by id
 * and kind rather than by name — an agent's name travels, a user's does not —
 * so a person is shown as their kind, which is what the platform actually
 * knows, rather than initials invented from an id. */
function Initials({ n }: { n: Notif }) {
  return (
    <span
      title={n.actor_name || n.actor_kind}
      style={{
        width: 22,
        height: 22,
        borderRadius: 99,
        flex: "none",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        background: "var(--line-2)",
        font: "600 10px var(--mono)",
        color: "var(--fg-dim)",
      }}
    >
      {n.actor_name
        ? n.actor_name.slice(0, 2).toUpperCase()
        : (n.actor_kind || "?").slice(0, 1).toUpperCase()}
    </span>
  );
}

/** groupByDay partitions rows into the design's TODAY / YESTERDAY headings.
 * Anything older than yesterday falls under an absolute date, so a list that
 * reaches back further than the artboard's two days still reads in order. */
function groupByDay(rows: Notif[]): [string, Notif[]][] {
  const out: [string, Notif[]][] = [];
  const today = new Date();
  const yesterday = new Date(Date.now() - 86400000);
  const sameDay = (a: Date, b: Date) =>
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate();
  for (const n of rows) {
    const d = new Date(n.created_at);
    const heading = sameDay(d, today)
      ? "TODAY"
      : sameDay(d, yesterday)
        ? "YESTERDAY"
        : d
            .toLocaleDateString(undefined, { month: "short", day: "numeric" })
            .toUpperCase();
    const last = out[out.length - 1];
    if (last && last[0] === heading) last[1].push(n);
    else out.push([heading, [n]]);
  }
  return out;
}

/** rel renders the design's relative time: "2h", "12m", "1d". */
function rel(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  const m = Math.floor(ms / 60000);
  if (m < 1) return "now";
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}

/** Notif is one row of GET /api/v1/orgs/{org}/inbox, as the edge renders it
 * in InboxJSON — mirror and keep in step. */
interface Notif {
  id: string;
  repo: string;
  reason: string;
  ref: string;
  title: string;
  body: string;
  actor_kind: string;
  actor_name: string;
  state: string;
  snoozed: boolean;
  created_at: string;
  link: string;
}

const groupLabel = {
  font: "600 11px var(--sans)",
  letterSpacing: ".08em",
  color: "var(--fg-muted)",
  marginBottom: 6,
} as const;

const groupHeading = {
  padding: "8px 14px 4px",
  font: "600 10px var(--sans)",
  letterSpacing: ".1em",
  color: "var(--fg-faint)",
  background: "var(--panel-2)",
} as const;

const ghostButton = {
  padding: "4px 10px",
  border: "1px solid var(--line-2)",
  borderRadius: 7,
  background: "transparent",
  color: "var(--fg-dim)",
  font: "12px var(--sans)",
  cursor: "pointer",
} as const;

const openButton = {
  padding: "5px 12px",
  border: "1px solid var(--line-2)",
  borderRadius: 7,
  font: "12px var(--sans)",
} as const;

const keyboardFooter = {
  marginTop: 12,
  font: "11px var(--mono)",
  color: "var(--fg-faint)",
  textAlign: "center",
} as const;
