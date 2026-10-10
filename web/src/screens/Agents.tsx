import { useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import {
  Async,
  Empty,
  Failed,
  Loading,
  Page,
  Panel,
  PanelHead,
  Pill,
  StatePill,
} from "../components/ui";
import { Dialog, NewButton } from "../components/Dialog";
import { ACTIVE_RUN_STATES } from "../components/CancelAgentRun";
import { timeAgo } from "./Home";
import type { Agent, AgentRun as Run } from "../lib/types";

/** ROLES are the roles a decomposition may assign work to. They match
 * swarm.DefaultAgentRoles, so an agent created here can actually be given a
 * subtask by the planner. */
const ROLES = [
  "implementer",
  "architect",
  "reviewer",
  "security",
  "test",
  "documentation",
];

/** AgentStats is the per-agent record the platform keeps: how many runs it has
 * started, how they ended, and what it has spent. tokens_used sums the spend
 * recorded when runs ended; token_limit sums the caps, including caps of runs
 * still going, so "used of caps" is the platform's own ratio with those two
 * definitions behind it, and is not re-derived here. */
interface AgentStats {
  agent_id: string;
  runs: number;
  succeeded: number;
  failed: number;
  over_budget: number;
  running: number;
  tokens_used: number;
  token_limit: number;
}

export function Agents() {
  const w = useWorkspace();
  const [selected, setSelected] = useState(0);
  const [creating, setCreating] = useState(false);
  const qc = useQueryClient();

  const create = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post(`/api/v1/orgs/${enc(w.org!)}/agents`, {
        name: v.name,
        role: v.role,
        model_ref: v.model_ref,
      }),
    onSuccess: () => {
      setCreating(false);
      qc.invalidateQueries();
    },
  });

  const agents = useQuery({
    queryKey: ["agents", w.org],
    queryFn: () =>
      api.get<{ agents: Agent[] }>(`/api/v1/orgs/${enc(w.org!)}/agents`),
    enabled: w.org !== null,
    refetchInterval: 5000,
  });

  const stats = useQuery({
    queryKey: ["agent-stats", w.org],
    queryFn: () =>
      api.get<{ stats: AgentStats[] }>(
        `/api/v1/orgs/${enc(w.org!)}/agents/stats`,
      ),
    enabled: w.org !== null,
    refetchInterval: 10_000,
  });

  const statFor = (id: string) =>
    stats.data?.stats.find((s) => s.agent_id === id);
  // The workspace guards against an org that is still loading; the roster
  // panel and the detail column render only once agents exist to show.
  const current =
    agents.data && agents.data.agents.length > 0
      ? agents.data.agents[Math.min(selected, agents.data.agents.length - 1)]!
      : null;

  return (
    <Page
      title="Agents"
      subtitle="Agents are organization members whose authority is a capability grant, not a token"
      actions={
        <NewButton label="New agent" onClick={() => setCreating(true)} />
      }
    >
      {creating ? (
        <Dialog
          title="New agent"
          submitLabel="Create"
          fields={[
            {
              name: "name",
              label: "Name",
              required: true,
              placeholder: "builder",
            },
            {
              name: "role",
              label: "Role",
              type: "select",
              options: ROLES,
              required: true,
            },
            {
              name: "model_ref",
              label: "Model",
              placeholder: "leave empty for the deployment's default",
              help: "The deployment's configured model is used when this is empty.",
            },
          ]}
          busy={create.isPending}
          error={create.error}
          onSubmit={(v) => create.mutate(v)}
          onClose={() => setCreating(false)}
        />
      ) : null}

      <Async query={agents}>
        {(d) => {
          if (d.agents.length === 0)
            return (
              <Empty>
                This organization has defined no agents.
                <br />
                Create one with{" "}
                <code style={{ font: "11px var(--mono)" }}>
                  nf agent create &lt;name&gt;
                </code>
                .
              </Empty>
            );
          const busy = d.agents.filter((a) => a.current_run_id !== "");
          return (
            <div
              style={{
                display: "grid",
                gridTemplateColumns: "minmax(0,360px) minmax(0,1fr)",
                gap: 14,
                alignItems: "start",
              }}
            >
              <Panel>
                <PanelHead title="AGENTS" count={d.agents.length}>
                  <span
                    style={{
                      font: "10px var(--sans)",
                      color: busy.length > 0 ? "var(--ok)" : "var(--fg-faint)",
                      marginLeft: "auto",
                    }}
                  >
                    {busy.length > 0
                      ? `${busy.length} working`
                      : "none working"}
                  </span>
                </PanelHead>
                {d.agents.map((a, i) => {
                  const isBusy = a.current_run_id !== "";
                  const rowSelected =
                    i === Math.min(selected, d.agents.length - 1);
                  return (
                    <button
                      key={a.id}
                      onClick={() => setSelected(i)}
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: 10,
                        width: "100%",
                        padding: "10px 14px",
                        border: "none",
                        borderBottom: "1px solid var(--line)",
                        background: rowSelected
                          ? "var(--accent-softer)"
                          : "transparent",
                        cursor: "pointer",
                        textAlign: "left",
                      }}
                    >
                      <span
                        style={{
                          width: 26,
                          height: 26,
                          borderRadius: 99,
                          background: isBusy ? "var(--accent)" : "#33415e",
                          display: "grid",
                          placeItems: "center",
                          font: "600 10px var(--sans)",
                          color: "#fff",
                          flex: "none",
                        }}
                      >
                        {initials(a.name)}
                      </span>
                      <span style={{ flex: 1, minWidth: 0 }}>
                        <span
                          style={{
                            display: "flex",
                            alignItems: "baseline",
                            gap: 8,
                          }}
                        >
                          <span style={{ font: "500 13px var(--sans)" }}>
                            {a.name}
                          </span>
                          {isBusy ? (
                            <span
                              style={{
                                marginLeft: "auto",
                                font: "11px var(--sans)",
                                color: "var(--fg-faint)",
                              }}
                            >
                              {timeAgo(a.busy_since)}
                            </span>
                          ) : null}
                        </span>
                        <span
                          style={{
                            display: "block",
                            font: "11px var(--mono)",
                            color: "var(--fg-muted)",
                            marginTop: 2,
                            overflow: "hidden",
                            textOverflow: "ellipsis",
                            whiteSpace: "nowrap",
                          }}
                        >
                          {isBusy && a.current_work_item_key
                            ? `${a.current_work_item_key} · ${a.role}`
                            : a.role}
                        </span>
                      </span>
                      <Pill
                        bg={
                          isBusy
                            ? "var(--ok-bg)"
                            : a.enabled
                              ? "var(--info-bg)"
                              : "rgba(139,145,160,.13)"
                        }
                        fg={
                          isBusy
                            ? "var(--ok)"
                            : a.enabled
                              ? "var(--link)"
                              : "var(--fg-muted)"
                        }
                      >
                        {isBusy
                          ? "working"
                          : a.enabled
                            ? "enabled"
                            : "disabled"}
                      </Pill>
                    </button>
                  );
                })}
              </Panel>

              {current ? (
                <AgentDetail
                  agent={current}
                  org={w.org!}
                  stats={statFor(current.id)}
                  statsError={stats.error}
                  statsLoading={stats.isLoading}
                />
              ) : null}
            </div>
          );
        }}
      </Async>
    </Page>
  );
}

function AgentDetail({
  agent,
  org,
  stats,
  statsError,
  statsLoading,
}: {
  agent: Agent;
  /** org is the workspace's org name, which is what every route takes; the
   * agent record's org_id is not that, and a uuid in the path would be a
   * different (failed) request. */
  org: string;
  stats: AgentStats | undefined;
  statsError: unknown;
  statsLoading: boolean;
}) {
  const isBusy = agent.current_run_id !== "";
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
      <Panel>
        <PanelHead>{agent.name.toUpperCase()}</PanelHead>
        <div style={{ padding: 14, display: "grid", gap: 10 }}>
          <KV k="Role" v={agent.role} />
          <KV k="Model" v={agent.model_ref || "the deployment's default"} />
          <KV k="Enabled" v={agent.enabled ? "yes" : "no"} />
          <KV k="Agent id" v={agent.id} mono />
          {isBusy ? (
            <KV
              k="Working"
              v={`${agent.current_work_item_key || "a run"} · ${timeAgo(agent.busy_since)}`}
              mono
            />
          ) : null}
        </div>
      </Panel>

      {isBusy ? <CurrentRun agent={agent} org={org} /> : null}

      <Panel>
        <PanelHead title="RUN HISTORY" />
        {statsLoading ? (
          <Loading />
        ) : statsError ? (
          <Failed error={statsError} />
        ) : !stats ? (
          <Empty>This agent has started no runs.</Empty>
        ) : (
          <div style={{ padding: 14, display: "grid", gap: 12 }}>
            <div
              style={{
                display: "grid",
                gridTemplateColumns: "repeat(auto-fit,minmax(110px,1fr))",
                gap: 12,
              }}
            >
              <Metric label="Runs" value={stats.runs} />
              <Metric
                label="Succeeded"
                value={stats.succeeded}
                tone="var(--ok)"
              />
              <Metric label="Failed" value={stats.failed} tone="var(--bad)" />
              <Metric
                label="Over budget"
                value={stats.over_budget}
                tone="var(--warn)"
              />
              <Metric
                label="Running"
                value={stats.running}
                tone="var(--link)"
              />
            </div>
            <BudgetLine used={stats.tokens_used} caps={stats.token_limit} />
          </div>
        )}
      </Panel>
    </div>
  );
}

/** CurrentRun is the one panel where the design's "Agents at work" row
 * grammar lives: which run, what it is doing, what it is allowed to spend.
 * The live spend the design draws ("62% of 400k tok") does not exist in this
 * deployment: the platform records a run's spend when the run ends, so the
 * panel shows the caps and the audit log instead of a percentage that would
 * be invented. */
function CurrentRun({ agent, org }: { agent: Agent; org: string }) {
  const base = `/api/v1/orgs/${enc(org)}/agent-runs/${enc(agent.current_run_id)}`;
  const run = useQuery({
    queryKey: ["agent-run", base],
    queryFn: () => api.get<Run>(base),
    refetchInterval: 5000,
  });
  const calls = useQuery({
    queryKey: ["agent-run-tools", base],
    queryFn: () => api.get<{ calls: ToolCall[] }>(`${base}/tools`),
    refetchInterval: 5000,
  });
  const latest =
    calls.data?.calls.length && calls.data.calls.length > 0
      ? calls.data.calls[calls.data.calls.length - 1]!
      : undefined;
  return (
    <Panel>
      <PanelHead title="CURRENT RUN">
        {run.data ? <StatePill state={run.data.state} /> : null}
        <div style={{ flex: 1 }} />
        <Link to={`/agent-runs/${enc(agent.current_run_id)}`}>Open run</Link>
      </PanelHead>
      <div style={{ padding: 14, display: "grid", gap: 10 }}>
        <KV k="Run" v={agent.current_run_id} mono />
        <KV k="Branch" v={run.data?.branch || "—"} mono />
        <KV k="Token cap" v={run.data ? tok(run.data.token_limit) : "…"} />
        {run.data && !ACTIVE_RUN_STATES.has(run.data.state) ? (
          <KV
            k="Recorded spend"
            v={`${tok(run.data.tokens_used)} tok${run.data.cost_used_micros > 0 ? ` · ${dollars(run.data.cost_used_micros)}` : ""}`}
          />
        ) : run.data ? (
          <KV
            k="Spend"
            v="recorded when the run ends; the tool log below shows what it is doing"
          />
        ) : null}
        <KV
          k="Latest tool call"
          v={
            calls.isLoading
              ? "…"
              : calls.error
                ? "the tool log could not be read"
                : latest
                  ? `${latest.tool} (${calls.data!.calls.length} recorded)`
                  : "no tool calls recorded yet"
          }
          mono
        />
        {latest && latest.error ? (
          <div style={{ font: "11px var(--mono)", color: "var(--bad)" }}>
            {latest.error}
          </div>
        ) : null}
      </div>
    </Panel>
  );
}

/** ToolCall is one row of the audit log: what tool ran, with what arguments
 * as the audit keeps them, and how it ended. */
interface ToolCall {
  tool: string;
  args: string;
  outcome: string;
  error: string;
  started_at: string;
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

function Metric({
  label,
  value,
  tone,
}: {
  label: string;
  value: number | string;
  tone?: string;
}) {
  return (
    <div>
      <div style={{ font: "600 18px var(--sans)", color: tone ?? "var(--fg)" }}>
        {value}
      </div>
      <div
        style={{
          font: "11px var(--sans)",
          color: "var(--fg-muted)",
          marginTop: 2,
        }}
      >
        {label}
      </div>
    </div>
  );
}

/** BudgetLine renders the platform's own ratio: recorded spend against summed
 * caps. Zero caps (a deployment that caps nothing) shows the spend alone —
 * a 0% bar next to real spend would be a lie about the limit, not about the
 * spend. */
function BudgetLine({ used, caps }: { used: number; caps: number }) {
  const pct = caps > 0 ? Math.min(100, Math.round((used / caps) * 100)) : null;
  return (
    <div>
      <div
        style={{
          display: "flex",
          alignItems: "baseline",
          gap: 8,
          marginBottom: 5,
        }}
      >
        <span style={{ font: "600 12px var(--sans)" }}>Budget</span>
        <span style={{ font: "11px var(--mono)", color: "var(--fg-dim)" }}>
          {compact(used)} of {compact(caps)} tok
          {pct !== null ? ` · ${pct}%` : " · no caps"}
        </span>
      </div>
      {pct !== null ? (
        <div
          style={{
            height: 4,
            borderRadius: 99,
            background: "var(--panel-2)",
            overflow: "hidden",
          }}
        >
          <div
            style={{
              width: `${pct}%`,
              height: "100%",
              background:
                pct > 90
                  ? "var(--bad)"
                  : pct > 70
                    ? "var(--warn)"
                    : "var(--accent)",
            }}
          />
        </div>
      ) : null}
    </div>
  );
}

function initials(name: string): string {
  const parts = name.split(/[\s-_]+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return parts[0]!.slice(0, 2).toUpperCase();
  return (parts[0]![0]! + parts[1]![0]!).toUpperCase();
}

function compact(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

function tok(n: number): string {
  return `${compact(n)} tok`;
}

/** micro-units of cost are 1e-6 of the currency unit; the design reads them
 * as dollars and so does this. */
function dollars(micros: number): string {
  return `$${(micros / 1_000_000).toFixed(2)}`;
}
