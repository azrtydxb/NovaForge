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
import { TYPES } from "./Work";
import { Dialog } from "../components/Dialog";
import {
  ACTIVE_RUN_STATES,
  CancelAgentRun,
} from "../components/CancelAgentRun";
import type {
  Agent,
  AgentRun,
  OrgMember,
  Subtask,
  WorkItem as Item,
} from "../lib/types";

/** RunSpend shows what a finished run used against what it was allowed, and
 * for a run that did not succeed, why it ended. An over-budget run names the
 * limit that stopped it; the spend is recorded when a run ends, so a run still
 * going shows its limits only. */
function RunSpend({ run }: { run: AgentRun }) {
  const ended = !ACTIVE_RUN_STATES.has(run.state);
  const parts = [
    ended
      ? `${run.tokens_used.toLocaleString()} / ${run.token_limit.toLocaleString()} tokens`
      : `limit ${run.token_limit.toLocaleString()} tokens`,
    `${Math.round(run.wallclock_limit_seconds / 60)} min wall clock`,
    run.cost_limit_micros > 0
      ? ended
        ? `${run.cost_used_micros.toLocaleString()} / ${run.cost_limit_micros.toLocaleString()} µ cost`
        : `limit ${run.cost_limit_micros.toLocaleString()} µ cost`
      : "no cost limit",
  ];
  const color =
    run.state === "over_budget"
      ? "var(--warn)"
      : run.state === "failed"
        ? "var(--bad)"
        : "var(--fg-faint)";
  return (
    <>
      <div
        style={{
          font: "11px var(--mono)",
          color: "var(--fg-faint)",
          marginTop: 2,
        }}
      >
        {parts.join(" · ")}
      </div>
      {run.end_reason ? (
        <div
          style={{
            font: "11px var(--sans)",
            color,
            marginTop: 3,
            lineHeight: 1.4,
          }}
        >
          {run.state === "over_budget" ? "Stopped over budget: " : "Ended: "}
          {run.end_reason}
        </div>
      ) : null}
    </>
  );
}

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
  const [editing, setEditing] = useState<Item | null>(null);
  const intent = (i: Item) => ({
    type: i.type,
    goal: i.goal,
    acceptance: i.acceptance ?? [],
    constraints: i.constraints ?? [],
    required_gates: i.required_gates ?? [],
  });
  const edit = useMutation({
    mutationFn: (v: Record<string, string>) => {
      if (!editing) throw new Error("No intent selected");
      const lines = (s: string | undefined) =>
        (s ?? "")
          .split("\n")
          .map((s) => s.trim())
          .filter(Boolean);
      return api.patch(base, {
        type: v.type,
        goal: v.goal,
        acceptance: lines(v.acceptance),
        constraints: lines(v.constraints),
        required_gates: lines(v.required_gates),
        expected: intent(editing),
      });
    },
    onSuccess: () => {
      setEditing(null);
      void qc.invalidateQueries();
    },
  });
  const transition = useMutation({
    mutationFn: (i: Item) =>
      api.post(`${base}/transitions`, {
        expected_state: i.state,
        to_state: i.state === "open" ? "blocked" : "open",
      }),
    onSuccess: () => {
      void qc.invalidateQueries();
    },
  });

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
      // An empty limit is left to the platform's default; a cost limit is
      // refused by a deployment that prices no tokens, and that refusal is
      // shown rather than a limit that could never be reached.
      const limit = (field: string, scale = 1) => {
        const raw = (v[field] ?? "").trim();
        if (raw === "") return 0;
        const n = Number(raw);
        if (!Number.isFinite(n) || n <= 0) {
          throw new Error(
            `${field.replace(/_/g, " ")} must be a positive number`,
          );
        }
        return Math.round(n * scale);
      };
      return api.post(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(repo)}/agent-runs`,
        {
          agent_id: agent.id,
          work_item_key: key,
          wallclock_limit_seconds: limit("wallclock_minutes", 60),
          token_limit: limit("token_limit"),
          cost_limit_micros: limit("cost_limit_micros"),
        },
      );
    },
    onSuccess: () => {
      setStarting(false);
      qc.invalidateQueries();
    },
  });

  // A Work Item is assignable to a person or an agent. The choices are the
  // organization's members and its agents, labelled by kind, so the id sent
  // is always one the platform issued rather than a typed name.
  const members = useQuery({
    queryKey: ["members", w.org],
    queryFn: () =>
      api.get<{ members: OrgMember[] }>(`/api/v1/orgs/${enc(w.org!)}/members`),
    enabled: w.org !== null,
  });
  const assignees = [
    ...(members.data?.members ?? []).map((m) => ({
      label: `person · ${m.username}`,
      id: m.user_id,
      kind: "user",
    })),
    ...(agents.data?.agents ?? []).map((a) => ({
      label: `agent · ${a.name}`,
      id: a.id,
      kind: "agent",
    })),
  ];
  const assigneeName = (id: string, kind: string) =>
    kind === "agent"
      ? `agent · ${agentName(id)}`
      : `person · ${members.data?.members.find((m) => m.user_id === id)?.username ?? id.slice(0, 8)}`;
  const [assigning, setAssigning] = useState(false);
  const assign = useMutation({
    mutationFn: (v: Record<string, string>) => {
      const choice = assignees.find((a) => a.label === v.assignee);
      if (!choice) throw new Error("pick someone to assign");
      return api.post(`${base}/assign`, {
        assignee_id: choice.id,
        assignee_kind: choice.kind,
      });
    },
    onSuccess: () => {
      setAssigning(false);
      qc.invalidateQueries({ queryKey: ["work-item"] });
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
          {item.data?.state === "open" &&
          item.data.execution_claimed === false &&
          item.data.maintenance_proposal === false ? (
            <button
              style={secondary}
              onClick={() => {
                edit.reset();
                setEditing(item.data!);
              }}
            >
              Edit intent
            </button>
          ) : null}
          {item.data &&
          item.data.execution_claimed === false &&
          item.data.maintenance_proposal === false &&
          item.data.assignee_kind !== "agent" &&
          ["open", "blocked"].includes(item.data.state) ? (
            <button
              style={secondary}
              disabled={transition.isPending}
              onClick={() => transition.mutate(item.data!)}
            >
              {item.data.state === "open" ? "Block work" : "Reopen work"}
            </button>
          ) : null}
          {item.data && assignees.length > 0 ? (
            <button onClick={() => setAssigning(true)} style={secondary}>
              Assign
            </button>
          ) : null}
          {item.data &&
          subtasks.data &&
          !subtasks.error &&
          subs.length === 0 ? (
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
          ) : item.data?.state === "open" &&
            !item.data.awaiting_approval &&
            enabledAgents.length > 0 ? (
            <button onClick={() => setStarting(true)} style={primary}>
              Start an Agent Run
            </button>
          ) : null}
        </div>
      }
    >
      {transition.error ? <Failed error={transition.error} /> : null}
      {editing ? (
        <Dialog
          title={`Edit ${key} intent`}
          submitLabel="Save intent"
          fields={[
            {
              name: "goal",
              label: "Goal",
              required: true,
              initialValue: editing.goal,
            },
            {
              name: "type",
              label: "Type",
              type: "select",
              options: TYPES,
              required: true,
              initialValue: editing.type,
            },
            {
              name: "acceptance",
              label: "Acceptance criteria",
              type: "textarea",
              initialValue: (editing.acceptance ?? []).join("\n"),
            },
            {
              name: "constraints",
              label: "Constraints",
              type: "textarea",
              initialValue: (editing.constraints ?? []).join("\n"),
            },
            {
              name: "required_gates",
              label: "Required gates",
              type: "textarea",
              initialValue: (editing.required_gates ?? []).join("\n"),
            },
          ]}
          busy={edit.isPending}
          error={edit.error}
          onSubmit={(v) => edit.mutate(v)}
          onClose={() => setEditing(null)}
        />
      ) : null}
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
            {
              name: "wallclock_minutes",
              label: "Wall-clock limit (minutes)",
              placeholder: "60",
            },
            {
              name: "token_limit",
              label: "Token limit",
              placeholder: "1000000",
            },
            {
              name: "cost_limit_micros",
              label: "Cost limit (micro-units)",
              placeholder: "none",
              help: "Only a deployment that prices its model's tokens can enforce a cost limit.",
            },
          ]}
          busy={startRun.isPending}
          error={startRun.error}
          onSubmit={(v) => startRun.mutate(v)}
          onClose={() => setStarting(false)}
        />
      ) : null}

      {assigning ? (
        <Dialog
          title={`Assign ${key}`}
          submitLabel="Assign"
          fields={[
            {
              name: "assignee",
              label: "Assignee",
              type: "select",
              options: assignees.map((a) => a.label),
              required: true,
              help: "Assigning an agent does not start it; start an Agent Run when you want it to work.",
            },
          ]}
          busy={assign.isPending}
          error={assign.error}
          onSubmit={(v) => assign.mutate(v)}
          onClose={() => setAssigning(false)}
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
                  <div
                    style={{
                      marginTop: 8,
                      font: "11px var(--mono)",
                      color: "var(--fg-muted)",
                    }}
                  >
                    {d.assignee_id
                      ? `assigned to ${assigneeName(d.assignee_id, d.assignee_kind)}`
                      : "unassigned"}
                  </div>
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
                          <Link
                            to={`/agent-runs/${enc(r.id)}`}
                            style={{ font: "500 12px var(--sans)" }}
                          >
                            {agentName(r.agent_id)} · inspect run
                          </Link>
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
                          <RunSpend run={r} />
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
            {subtasks.error ? (
              <Failed error={subtasks.error} />
            ) : subtasks.isLoading ? (
              <Empty>Loading subtasks…</Empty>
            ) : subs.length === 0 ? (
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
              aria-label="Work Item comment"
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
