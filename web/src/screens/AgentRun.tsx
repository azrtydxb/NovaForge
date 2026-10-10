import { useEffect, useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import type { Agent, AgentRun as Run } from "../lib/types";
import {
  ACTIVE_RUN_STATES,
  CancelAgentRun,
} from "../components/CancelAgentRun";
import {
  Async,
  Empty,
  Failed,
  Page,
  Panel,
  PanelHead,
  StatePill,
} from "../components/ui";
import { timeAgo } from "./Home";

/** ToolCall is one row of the audit log: what tool ran, with what arguments
 * as the audit keeps them, and how it ended. */
interface ToolCall {
  tool: string;
  args: string;
  outcome: string;
  error: string;
  started_at: string;
}

/** Observable events and durable calls are separate: reconnecting is not a
 * guarantee of replay, and a live tool event is not proof it completed. The
 * durable tools endpoint is the record; the stream is what is happening. */
export function AgentRun() {
  const { id = "" } = useParams();
  const w = useWorkspace();
  const qc = useQueryClient();
  const base = `/api/v1/orgs/${enc(w.org!)}/agent-runs/${enc(id)}`;
  const run = useQuery({
    queryKey: ["agent-run", base],
    queryFn: () => api.get<Run>(base),
    enabled: !!w.org,
    refetchInterval: 5000,
  });
  const calls = useQuery({
    queryKey: ["agent-run-tools", base],
    queryFn: () => api.get<{ calls: ToolCall[] }>(`${base}/tools`),
    enabled: !!w.org,
    refetchInterval: 5000,
  });
  const agents = useQuery({
    queryKey: ["agents", w.org],
    queryFn: () =>
      api.get<{ agents: Agent[] }>(`/api/v1/orgs/${enc(w.org!)}/agents`),
    enabled: !!w.org,
  });
  const live = !!run.data && ACTIVE_RUN_STATES.has(run.data.state);
  const nameFor = (id: string): string | undefined =>
    agents.data?.agents.find((a) => a.id === id)?.name;
  const [streamError, setStreamError] = useState<unknown>(null);
  const [connected, setConnected] = useState(false);
  const [latest, setLatest] = useState<unknown>(null);
  const [attempt, setAttempt] = useState(0);
  // The position is a ref, not state: a reconnect must resume from the last
  // event actually handled, and storing it in state would restart the effect on
  // every event and so tear the stream down to rebuild it.
  const cursor = useRef<{ base: string; id?: string }>({ base });
  useEffect(() => {
    if (!live) return;
    // A position belongs to one run's stream. Without this, opening a second
    // run would resume it from the first run's last event.
    if (cursor.current.base !== base) cursor.current = { base };
    const controller = new AbortController();
    setConnected(false);
    setStreamError(null);
    setLatest(null);
    void api
      .events(
        `${base}/events`,
        controller.signal,
        (event, data, id) => {
          if (event === "error")
            throw new Error(
              typeof data === "object" && data && "error" in data
                ? String(data.error)
                : "Event stream failed",
            );
          setConnected(true);
          if (event === "message") {
            setLatest(data);
            void qc.invalidateQueries({ queryKey: ["agent-run", base] });
            void qc.invalidateQueries({ queryKey: ["agent-run-tools", base] });
          }
          // Only a frame that carries an id advances the position; a
          // heartbeat or a state change without one must not reset it.
          if (id) cursor.current = { base, id };
        },
        cursor.current.id,
      )
      .then(() => {
        if (!controller.signal.aborted) {
          setConnected(false);
          setStreamError(
            new Error(
              "Event stream ended. Durable evidence continues to refresh.",
            ),
          );
        }
      })
      .catch((e: unknown) => {
        if (!controller.signal.aborted) {
          setConnected(false);
          setStreamError(e);
        }
      });
    return () => controller.abort();
  }, [base, live, attempt, qc]);
  const terminal = !!run.data && !ACTIVE_RUN_STATES.has(run.data.state);
  return (
    <Page
      title="Agent Run"
      subtitle={id}
      actions={
        w.org && live ? <CancelAgentRun org={w.org} runId={id} /> : undefined
      }
    >
      <Async query={run}>
        {(r) => (
          <>
            <Panel style={{ padding: 14, marginBottom: 14 }}>
              <div
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: 10,
                  flexWrap: "wrap",
                }}
              >
                <StatePill state={r.state} />
                <code style={{ font: "12px var(--mono)" }}>{r.branch}</code>
                <div style={{ flex: 1 }} />
                {terminal ? null : (
                  <span
                    role="status"
                    style={{
                      font: "11px var(--sans)",
                      color: "var(--ok)",
                      animation: "nfpulse 1.6s infinite",
                    }}
                  >
                    elapsed {timeAgo(r.started_at)}
                  </span>
                )}
              </div>
              <div
                style={{
                  display: "grid",
                  gap: 8,
                  marginTop: 12,
                }}
              >
                <KV
                  k="Agent"
                  v={nameFor(r.agent_id) ?? r.agent_id}
                  mono={nameFor(r.agent_id) === undefined}
                />
                <KV k="Sponsor" v={r.sponsor_id} mono />
                {/* A run carries the Work Item's id, not its key, and this
                 * deployment exposes no id-to-key lookup, so the item cannot
                 * be linked without inventing the path to it. */}
                <KV k="Work item" v={r.work_item_id || "—"} mono />
                <KV
                  k={terminal ? "Ran" : "Running since"}
                  v={`${r.started_at ? new Date(r.started_at).toLocaleString() : "—"}${r.ended_at ? ` → ${new Date(r.ended_at).toLocaleString()}` : ""}`}
                />
              </div>
            </Panel>

            <Panel style={{ padding: 14, marginBottom: 14 }}>
              <PanelHead title="BUDGET" />
              <div style={{ padding: 14, display: "grid", gap: 8 }}>
                <KV k="Caps" v={capLine(r)} />
                {terminal ? (
                  <KV k="Recorded spend" v={spendLine(r)} />
                ) : (
                  <KV
                    k="Spend"
                    v="recorded when the run ends; the tool log below is the live evidence"
                  />
                )}
                {r.end_reason ? <KV k="End reason" v={r.end_reason} /> : null}
              </div>
            </Panel>
          </>
        )}
      </Async>
      {live ? (
        <Panel style={{ marginBottom: 14 }}>
          <PanelHead title="LIVE EVENTS">
            <span
              role="status"
              style={{
                font: "10px var(--mono)",
                color: connected ? "var(--ok)" : "var(--warn)",
              }}
            >
              {connected ? "connected" : "disconnected"}
            </span>
            <div style={{ flex: 1 }} />
            <span
              style={{ font: "10px var(--sans)", color: "var(--fg-faint)" }}
            >
              durable evidence refreshes every five seconds
            </span>
          </PanelHead>
          <div style={{ padding: 14 }}>
            {streamError ? (
              <div style={{ marginBottom: 10 }}>
                <Failed error={streamError} />
                <button
                  onClick={() => setAttempt((n) => n + 1)}
                  style={{
                    marginTop: 8,
                    background: "transparent",
                    border: "1px solid var(--line-2)",
                    borderRadius: 6,
                    color: "var(--fg-muted)",
                    font: "11px var(--sans)",
                    padding: "3px 9px",
                    cursor: "pointer",
                  }}
                >
                  Reconnect events
                </button>
              </div>
            ) : null}
            {latest === null ? (
              <div
                style={{
                  font: "12px var(--sans)",
                  color: "var(--fg-faint)",
                }}
              >
                A connected stream replays nothing by itself: what is recorded
                so far is in the tool log below.
              </div>
            ) : (
              <LiveEvent data={latest} />
            )}
          </div>
        </Panel>
      ) : null}

      <Panel>
        <PanelHead
          title="RECORDED TOOL CALLS"
          count={calls.data?.calls.length ?? 0}
        />
        <Async query={calls}>
          {(d) =>
            d.calls.length === 0 ? (
              <Empty>No tool calls recorded yet.</Empty>
            ) : (
              d.calls.map((c, i) => (
                <ToolRow key={`${c.started_at}/${i}`} c={c} />
              ))
            )
          }
        </Async>
      </Panel>
    </Page>
  );
}

/** LiveEvent renders the newest observed frame. A tool_call frame shows the
 * call as the audit will record it; a state_change frame shows the
 * transition; anything else falls back to the frame itself rather than being
 * silently dropped. None of it is reasoning: the stream carries observable
 * actions only. */
function LiveEvent({ data }: { data: unknown }) {
  const d = (data ?? {}) as {
    at?: string;
    tool_call?: ToolCall;
    state_change?: { from_state: string; to_state: string };
  };
  if (d.tool_call) {
    return (
      <div>
        <div style={{ font: "11px var(--sans)", color: "var(--fg-faint)" }}>
          latest observed event (not a replay guarantee) ·{" "}
          {d.at ? timeAgo(d.at) : ""}
        </div>
        <ToolRow c={d.tool_call} />
      </div>
    );
  }
  if (d.state_change) {
    return (
      <div style={{ font: "12px var(--sans)" }} role="status">
        state {d.state_change.from_state || "?"} →{" "}
        <b>{d.state_change.to_state || "?"}</b>
        {d.at ? (
          <span style={{ color: "var(--fg-faint)" }}> · {timeAgo(d.at)}</span>
        ) : null}
      </div>
    );
  }
  return (
    <pre
      style={{ whiteSpace: "pre-wrap", font: "11px var(--mono)", margin: 0 }}
    >
      {JSON.stringify(data, null, 2)}
    </pre>
  );
}

function ToolRow({ c }: { c: ToolCall }) {
  return (
    <div
      style={{
        padding: "12px 14px",
        borderBottom: "1px solid var(--line)",
      }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <code style={{ font: "600 12px var(--mono)" }}>{c.tool}</code>
        <StatePill state={c.outcome} />
        <div style={{ flex: 1 }} />
        <span
          title={c.started_at}
          style={{ font: "11px var(--sans)", color: "var(--fg-faint)" }}
        >
          {timeAgo(c.started_at)}
        </span>
      </div>
      <ArgsView raw={c.args} />
      {c.error ? (
        <div
          style={{
            font: "11px var(--mono)",
            color: "var(--bad)",
            marginTop: 6,
            overflowWrap: "anywhere",
          }}
        >
          {c.error}
        </div>
      ) : null}
    </div>
  );
}

/** ArgsView discloses each argument exactly as far as the audit log keeps it
 * (internal/tools/audit_args.go): an identifier is kept verbatim and shown
 * verbatim; content is kept only as a byte length and a digest, so the row
 * shows the digest rather than pretending to show the bytes. Anything the
 * reducer did not recognize as an object is shown as it was. */
function ArgsView({ raw }: { raw: string }) {
  let fields: [string, unknown][] | null = null;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (parsed !== null && typeof parsed === "object" && !Array.isArray(parsed))
      fields = Object.entries(parsed as Record<string, unknown>);
  } catch {
    fields = null;
  }
  if (fields === null)
    return (
      <pre
        style={{
          whiteSpace: "pre-wrap",
          overflowWrap: "anywhere",
          font: "11px var(--mono)",
          color: "var(--fg-dim)",
          margin: "6px 0 0",
        }}
      >
        {raw}
      </pre>
    );
  return (
    <div
      style={{
        font: "11px var(--mono)",
        color: "var(--fg-dim)",
        marginTop: 6,
        display: "grid",
        gap: 2,
      }}
    >
      {fields.map(([k, v]) => {
        const digest =
          v !== null &&
          typeof v === "object" &&
          !Array.isArray(v) &&
          typeof (v as Record<string, unknown>).sha256 === "string" &&
          typeof (v as Record<string, unknown>).bytes === "number"
            ? (v as { sha256: string; bytes: number })
            : null;
        return (
          <div key={k} style={{ overflowWrap: "anywhere" }}>
            <span style={{ color: "var(--fg-muted)" }}>{k}: </span>
            {digest ? (
              <span title={digest.sha256}>
                ⟨{digest.bytes} bytes · sha256:{digest.sha256.slice(0, 16)}…⟩
              </span>
            ) : (
              <span>
                {truncate(
                  typeof v === "string" ? v : (JSON.stringify(v) ?? "null"),
                )}
              </span>
            )}
          </div>
        );
      })}
    </div>
  );
}

function truncate(s: string): string {
  return s.length > 200 ? `${s.slice(0, 200)}…` : s;
}

function KV({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <div style={{ display: "flex", gap: 12 }}>
      <span
        style={{
          width: 110,
          flex: "none",
          font: "11px var(--sans)",
          color: "var(--fg-muted)",
        }}
      >
        {k}
      </span>
      <span
        style={{
          font: mono ? "11px var(--mono)" : "12px var(--sans)",
          color: "var(--fg-dim)",
          wordBreak: "break-all",
        }}
      >
        {v}
      </span>
    </div>
  );
}

/** capLine renders what the run was allowed. A cap of zero means the run has
 * none of that kind, and says so rather than reading as a limit of nothing. */
function capLine(r: Run): string {
  const parts = [`${tok(r.token_limit)} tok`];
  parts.push(fmtDuration(r.wallclock_limit_seconds));
  if (r.cost_limit_micros > 0) parts.push(dollars(r.cost_limit_micros));
  return parts.join(" · ");
}

/** spendLine is shown only for a run that ended: the store records a run's
 * spend when it ends, so a running run's zero would be the absence of a
 * record, not evidence of zero spend. */
function spendLine(r: Run): string {
  const parts = [`${tok(r.tokens_used)} tok`];
  if (r.token_limit > 0)
    parts.push(`${Math.round((r.tokens_used / r.token_limit) * 100)}% of cap`);
  if (r.cost_used_micros > 0) {
    let c = dollars(r.cost_used_micros);
    if (r.cost_limit_micros > 0)
      c += ` (${Math.round((r.cost_used_micros / r.cost_limit_micros) * 100)}% of cap)`;
    parts.push(c);
  }
  return parts.join(" · ");
}

function fmtDuration(seconds: number): string {
  if (seconds <= 0) return "no wall-clock cap";
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (h > 0) return m > 0 ? `${h}h ${m}m wall-clock` : `${h}h wall-clock`;
  return `${m}m wall-clock`;
}

function compact(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

function tok(n: number): string {
  return n > 0 ? `${compact(n)} tok` : "no cap";
}

function dollars(micros: number): string {
  return `$${(micros / 1_000_000).toFixed(2)}`;
}
