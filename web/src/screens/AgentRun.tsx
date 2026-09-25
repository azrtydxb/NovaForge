import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import type { AgentRun as Run } from "../lib/types";
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

interface ToolCall {
  tool: string;
  args: string;
  outcome: string;
  error: string;
  started_at: string;
}

/** Observable events and durable calls are separate: reconnecting is not a
 * guarantee of replay, and a live tool event is not proof it completed. */
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
  const live = !!run.data && ACTIVE_RUN_STATES.has(run.data.state);
  const [streamError, setStreamError] = useState<unknown>(null);
  const [connected, setConnected] = useState(false);
  const [latest, setLatest] = useState<unknown>(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!live) return;
    const controller = new AbortController();
    setConnected(false);
    setStreamError(null);
    setLatest(null);
    void api
      .events(`${base}/events`, controller.signal, (event, data) => {
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
      })
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
          <Panel style={{ padding: 14, marginBottom: 14 }}>
            <StatePill state={r.state} /> <code>{r.branch}</code>
            <p>
              Agent {r.agent_id} · Human sponsor {r.sponsor_id}
            </p>
            <p>
              Token limit {r.token_limit.toLocaleString()} · Wall-clock limit{" "}
              {r.wallclock_limit_seconds.toLocaleString()} seconds
            </p>
            {!ACTIVE_RUN_STATES.has(r.state) ? (
              <p>
                Recorded spend: {r.tokens_used.toLocaleString()} tokens ·{" "}
                {r.cost_used_micros.toLocaleString()} micro-units
              </p>
            ) : null}
            {r.end_reason ? <p>{r.end_reason}</p> : null}
          </Panel>
        )}
      </Async>
      {live ? (
        <Panel style={{ padding: 14, marginBottom: 14 }}>
          <div role="status">
            {connected ? "Live event connection" : "Live events disconnected"} ·
            Durable evidence refreshes every five seconds.
          </div>
          {streamError ? (
            <>
              <Failed error={streamError} />
              <button onClick={() => setAttempt((n) => n + 1)}>
                Reconnect events
              </button>
            </>
          ) : null}
          {latest ? (
            <>
              <p>Latest observed event (not a replay guarantee)</p>
              <pre style={{ whiteSpace: "pre-wrap" }}>
                {JSON.stringify(latest, null, 2)}
              </pre>
            </>
          ) : null}
        </Panel>
      ) : null}
      <Panel>
        <PanelHead>RECORDED TOOL CALLS</PanelHead>
        <Async query={calls}>
          {(d) =>
            d.calls.length === 0 ? (
              <Empty>No tool calls recorded yet.</Empty>
            ) : (
              d.calls.map((c, i) => (
                <div
                  key={`${c.started_at}/${i}`}
                  style={{ padding: 14, borderBottom: "1px solid var(--line)" }}
                >
                  <code>{c.tool}</code> <StatePill state={c.outcome} />{" "}
                  <span>{c.started_at}</span>
                  <pre
                    style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}
                  >
                    {c.error || c.args}
                  </pre>
                </div>
              ))
            )
          }
        </Async>
      </Panel>
    </Page>
  );
}
