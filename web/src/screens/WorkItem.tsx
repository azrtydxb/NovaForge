import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import {
  Async,
  Empty,
  Failed,
  Page,
  Panel,
  PanelHead,
  StatePill,
} from "../components/ui";
import { Dialog } from "../components/Dialog";
import {
  ACTIVE_RUN_STATES,
  CancelAgentRun,
} from "../components/CancelAgentRun";
import type { Agent, AgentRun, Subtask, WorkItem as Item } from "../lib/types";

interface Comment {
  id: string;
  author_id: string;
  author_kind: string;
  body: string;
  created_at: string;
}

/** WorkItemDetail is the Work Item itself: what is being asked for, what must
 * be true when it is done, its decomposition, and the discussion on it.
 *
 * The discussion matters more than it looks: it is where a person corrects an
 * agent, and where an agent records what it decided. Both write to the same
 * thread through the same RPC, so a reader sees one history rather than two. */
export function WorkItemDetail() {
  const { repo = "", key = "" } = useParams();
  const w = useWorkspace();
  const qc = useQueryClient();
  const [starting, setStarting] = useState(false);
  const [body, setBody] = useState("");

  const base = `/api/v1/orgs/${enc(w.org ?? "")}/repos/${enc(repo)}/work/${enc(key)}`;

  const item = useQuery({
    queryKey: ["work-item", w.org, repo, key],
    queryFn: () => api.get<Item>(base),
    enabled: w.org !== null,
  });

  const subtasks = useQuery({
    queryKey: ["subtasks", w.org, repo, key],
    queryFn: () => api.get<{ subtasks: Subtask[] }>(`${base}/subtasks`),
    enabled: w.org !== null,
  });

  const comments = useQuery({
    queryKey: ["comments", w.org, repo, key],
    queryFn: () => api.get<{ comments: Comment[] }>(`${base}/comments`),
    enabled: w.org !== null,
  });

  const agents = useQuery({
    queryKey: ["agents", w.org],
    queryFn: () =>
      api.get<{ agents: Agent[] }>(`/api/v1/orgs/${enc(w.org!)}/agents`),
    enabled: w.org !== null,
  });
  const enabledAgents = (agents.data?.agents ?? []).filter((a) => a.enabled);

  const runs = useQuery({
    queryKey: ["agent-runs", w.org, repo, key],
    queryFn: () => api.get<{ runs: AgentRun[] }>(`${base}/agent-runs`),
    enabled: w.org !== null,
    // A live run moves on its own; follow it while any is still going.
    refetchInterval: (q) =>
      q.state.data?.runs.some((r) => ACTIVE_RUN_STATES.has(r.state))
        ? 5_000
        : false,
  });
  const agentName = (id: string) =>
    agents.data?.agents.find((a) => a.id === id)?.name ?? id.slice(0, 8);

  const comment = useMutation({
    mutationFn: () => api.post(`${base}/comments`, { body }),
    onSuccess: () => {
      setBody("");
      qc.invalidateQueries({ queryKey: ["comments"] });
    },
  });

  const decompose = useMutation({
    mutationFn: () => api.post(`${base}/decompose`, {}),
    onSuccess: () => qc.invalidateQueries(),
  });

  const startRun = useMutation({
    mutationFn: (v: Record<string, string>) => {
      const agent = enabledAgents.find((a) => a.name === v.agent);
      if (!agent) throw new Error("pick an agent");
      return api.post(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(repo)}/agent-runs`,
        { agent_id: agent.id, work_item_key: key },
      );
    },
    onSuccess: () => {
      setStarting(false);
      qc.invalidateQueries();
    },
  });

  const subs = subtasks.data?.subtasks ?? [];

  return (
    <Page
      title={`${key} · ${item.data?.goal ?? ""}`}
      subtitle={`${repo} · ${item.data?.type ?? ""}`}
      actions={
        <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
          {item.data ? <StatePill state={item.data.state} /> : null}
          {subs.length === 0 ? (
            <button
              onClick={() => decompose.mutate()}
              disabled={decompose.isPending}
              style={secondary}
            >
              {decompose.isPending ? "Decomposing…" : "Decompose"}
            </button>
          ) : null}
          {item.data?.awaiting_approval ? (
            // Agent-runtime refuses a run on an unapproved proposal; the way
            // forward is the approval, not a Start button that would fail.
            <Link to="/maintenance" style={secondary}>
              Awaiting approval · Maintenance
            </Link>
          ) : enabledAgents.length > 0 ? (
            <button onClick={() => setStarting(true)} style={primary}>
              Start an Agent Run
            </button>
          ) : null}
        </div>
      }
    >
      {starting ? (
        <Dialog
          title={`Start an Agent Run on ${key}`}
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
          onClose={() => setStarting(false)}
        />
      ) : null}

      {decompose.error ? (
        <div style={{ marginBottom: 14 }}>
          <Failed error={decompose.error} />
        </div>
      ) : null}

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "minmax(0,1fr) minmax(0,1fr)",
          gap: 14,
        }}
      >
        <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
          <Panel>
            <PanelHead>WHAT IS BEING ASKED FOR</PanelHead>
            <Async query={item}>
              {(d) => (
                <div style={{ padding: 14 }}>
                  <div style={{ font: "14px/1.6 var(--sans)" }}>{d.goal}</div>
                  <List title="Acceptance criteria" items={d.acceptance} />
                  <List title="Constraints" items={d.constraints} />
                  <List title="Required gates" items={d.required_gates} />
                </div>
              )}
            </Async>
          </Panel>

          <Panel>
            <PanelHead>
              AGENT RUNS
              <span style={{ color: "var(--fg-faint)" }}>
                {runs.data?.runs.length ?? ""}
              </span>
            </PanelHead>
            <Async query={runs}>
              {(d) =>
                d.runs.length === 0 ? (
                  <Empty>No Agent Run has been started against {key}.</Empty>
                ) : (
                  <>
                    {d.runs.map((r) => (
                      <div
                        key={r.id}
                        style={{
                          display: "flex",
                          alignItems: "center",
                          gap: 10,
                          padding: "9px 14px",
                          borderBottom: "1px solid var(--line)",
                        }}
                      >
                        <span style={{ flex: 1, minWidth: 0 }}>
                          <div style={{ font: "500 12px var(--sans)" }}>
                            {agentName(r.agent_id)}
                          </div>
                          <div
                            style={{
                              font: "11px var(--mono)",
                              color: "var(--fg-faint)",
                              marginTop: 2,
                              overflow: "hidden",
                              textOverflow: "ellipsis",
                              whiteSpace: "nowrap",
                            }}
                          >
                            {r.branch}
                            {r.started_at
                              ? ` · ${r.started_at.replace("T", " ").slice(0, 19)}`
                              : ""}
                          </div>
                        </span>
                        {ACTIVE_RUN_STATES.has(r.state) && w.org ? (
                          <CancelAgentRun org={w.org} runId={r.id} />
                        ) : null}
                        <StatePill state={r.state} />
                      </div>
                    ))}
                  </>
                )
              }
            </Async>
          </Panel>

          <Panel>
            <PanelHead>
              SUBTASKS
              <span style={{ color: "var(--fg-faint)" }}>{subs.length}</span>
            </PanelHead>
            {subs.length === 0 ? (
              <Empty>
                Not decomposed.
                <br />
                Decompose breaks this into dependency-ordered subtasks across
                specialized agents.
              </Empty>
            ) : (
              subs.map((s) => (
                <div
                  key={s.id}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: 10,
                    padding: "9px 14px",
                    borderBottom: "1px solid var(--line)",
                  }}
                >
                  <Link
                    to={`/work/${enc(repo)}/${enc(s.key)}`}
                    style={{ width: 70, font: "600 11px var(--mono)" }}
                  >
                    {s.key}
                  </Link>
                  <span style={{ flex: 1, font: "12px var(--sans)" }}>
                    {s.goal}
                  </span>
                  <span
                    style={{
                      font: "10px var(--mono)",
                      color: s.ready ? "var(--ok)" : "var(--fg-faint)",
                    }}
                  >
                    {s.ready ? "ready" : "blocked"}
                  </span>
                  <StatePill state={s.state} />
                </div>
              ))
            )}
          </Panel>
        </div>

        <Panel>
          <PanelHead>DISCUSSION</PanelHead>
          <Async query={comments}>
            {(d) =>
              d.comments.length === 0 ? (
                <Empty>
                  Nothing recorded yet.
                  <br />
                  This is where a person corrects an agent, and where an agent
                  records what it decided.
                </Empty>
              ) : (
                <>
                  {d.comments.map((c) => (
                    <div
                      key={c.id}
                      style={{
                        padding: "11px 14px",
                        borderBottom: "1px solid var(--line)",
                      }}
                    >
                      <div
                        style={{
                          display: "flex",
                          alignItems: "center",
                          gap: 8,
                          marginBottom: 5,
                        }}
                      >
                        <span
                          style={{
                            font: "600 10px var(--mono)",
                            color:
                              c.author_kind === "agent"
                                ? "var(--violet)"
                                : "var(--link)",
                          }}
                        >
                          {c.author_kind}
                        </span>
                        <span
                          style={{
                            font: "10px var(--mono)",
                            color: "var(--fg-faint)",
                          }}
                        >
                          {c.created_at.replace("T", " ").slice(0, 19)}
                        </span>
                      </div>
                      <div
                        style={{
                          font: "13px/1.6 var(--sans)",
                          color: "var(--fg-dim)",
                          whiteSpace: "pre-wrap",
                        }}
                      >
                        {c.body}
                      </div>
                    </div>
                  ))}
                </>
              )
            }
          </Async>

          <form
            onSubmit={(e) => {
              e.preventDefault();
              if (body.trim()) comment.mutate();
            }}
            style={{ padding: 14 }}
          >
            <textarea
              value={body}
              onChange={(e) => setBody(e.target.value)}
              placeholder="Record a decision, or correct an agent…"
              rows={3}
              style={{
                width: "100%",
                padding: "8px 10px",
                background: "var(--bg)",
                border: "1px solid var(--line-2)",
                borderRadius: 7,
                color: "var(--fg)",
                font: "13px var(--sans)",
                outline: "none",
                resize: "vertical",
              }}
            />
            {comment.error ? (
              <div style={{ marginTop: 10 }}>
                <Failed error={comment.error} />
              </div>
            ) : null}
            <button
              type="submit"
              disabled={comment.isPending || !body.trim()}
              style={{
                ...primary,
                marginTop: 10,
                opacity: body.trim() ? 1 : 0.5,
              }}
            >
              {comment.isPending ? "Saving…" : "Comment"}
            </button>
          </form>
        </Panel>
      </div>
    </Page>
  );
}

function List({
  title,
  items,
}: {
  title: string;
  items: string[] | undefined;
}) {
  if (!items || items.length === 0) return null;
  return (
    <div style={{ marginTop: 14 }}>
      <div
        style={{
          font: "600 10px var(--sans)",
          letterSpacing: ".08em",
          color: "var(--fg-muted)",
          marginBottom: 6,
        }}
      >
        {title.toUpperCase()}
      </div>
      <ul style={{ margin: 0, paddingLeft: 18 }}>
        {items.map((i) => (
          <li
            key={i}
            style={{ font: "12px/1.7 var(--sans)", color: "var(--fg-dim)" }}
          >
            {i}
          </li>
        ))}
      </ul>
    </div>
  );
}

const primary: React.CSSProperties = {
  padding: "7px 14px",
  background: "var(--accent)",
  border: "none",
  borderRadius: 8,
  color: "#fff",
  font: "600 12px var(--sans)",
  cursor: "pointer",
};

const secondary: React.CSSProperties = {
  padding: "7px 14px",
  background: "transparent",
  border: "1px solid var(--line-2)",
  borderRadius: 8,
  color: "var(--fg-dim)",
  font: "12px var(--sans)",
  cursor: "pointer",
};
