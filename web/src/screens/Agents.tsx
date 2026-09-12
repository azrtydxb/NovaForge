import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import {
  Async,
  Empty,
  Page,
  Panel,
  PanelHead,
  StatePill,
} from "../components/ui";
import { Dialog, NewButton } from "../components/Dialog";

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
import type { Agent } from "../lib/types";

/** AgentStats is the per-agent record the platform keeps: how many runs it has
 * started, how they ended, and what it has spent. */
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
  });

  const stats = useQuery({
    queryKey: ["agent-stats", w.org],
    queryFn: () =>
      api.get<{ stats: AgentStats[] }>(
        `/api/v1/orgs/${enc(w.org!)}/agents/stats`,
      ),
    enabled: w.org !== null,
  });

  const statFor = (id: string) =>
    stats.data?.stats.find((s) => s.agent_id === id);

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
        {(d) =>
          d.agents.length === 0 ? (
            <Empty>
              This organization has defined no agents.
              <br />
              Create one with{" "}
              <code style={{ font: "11px var(--mono)" }}>
                nf agent create &lt;name&gt;
              </code>
              .
            </Empty>
          ) : (
            <div
              style={{
                display: "grid",
                gridTemplateColumns: "minmax(0,340px) minmax(0,1fr)",
                gap: 14,
              }}
            >
              <Panel>
                <PanelHead>AGENTS</PanelHead>
                {d.agents.map((a, i) => {
                  const s = statFor(a.id);
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
                        background:
                          selected === i
                            ? "rgba(77,127,255,.1)"
                            : "transparent",
                        cursor: "pointer",
                        textAlign: "left",
                      }}
                    >
                      <span
                        style={{
                          width: 26,
                          height: 26,
                          borderRadius: 7,
                          background: "#33415e",
                          display: "grid",
                          placeItems: "center",
                          font: "600 10px var(--sans)",
                          flex: "none",
                        }}
                      >
                        {initials(a.name)}
                      </span>
                      <span style={{ flex: 1 }}>
                        <div style={{ font: "500 13px var(--sans)" }}>
                          {a.name}
                        </div>
                        <div
                          style={{
                            font: "11px var(--mono)",
                            color: "var(--fg-faint)",
                            marginTop: 2,
                          }}
                        >
                          {a.role}
                          {s && s.running > 0 ? ` · ${s.running} running` : ""}
                        </div>
                      </span>
                      <StatePill state={a.enabled ? "running" : "cancelled"} />
                    </button>
                  );
                })}
              </Panel>

              <AgentDetail
                agent={d.agents[Math.min(selected, d.agents.length - 1)]!}
                stats={statFor(
                  d.agents[Math.min(selected, d.agents.length - 1)]!.id,
                )}
                statsError={stats.error}
              />
            </div>
          )
        }
      </Async>
    </Page>
  );
}

function AgentDetail({
  agent,
  stats,
  statsError,
}: {
  agent: Agent;
  stats: AgentStats | undefined;
  statsError: unknown;
}) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
      <Panel>
        <PanelHead>{agent.name.toUpperCase()}</PanelHead>
        <div style={{ padding: 14, display: "grid", gap: 10 }}>
          <KV k="Role" v={agent.role} />
          <KV k="Model" v={agent.model_ref || "the deployment's default"} />
          <KV k="Enabled" v={agent.enabled ? "yes" : "no"} />
          <KV k="Agent id" v={agent.id} mono />
        </div>
      </Panel>

      <Panel>
        <PanelHead>RUN HISTORY</PanelHead>
        {statsError ? (
          <Empty>Run statistics are not available in this deployment.</Empty>
        ) : !stats ? (
          <Empty>This agent has started no runs.</Empty>
        ) : (
          <div
            style={{
              padding: 14,
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
            <Metric label="Running" value={stats.running} tone="var(--link)" />
            <Metric
              label="Tokens used"
              value={compact(stats.tokens_used)}
              tone="var(--fg-dim)"
            />
          </div>
        )}
      </Panel>
    </div>
  );
}

function KV({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <div style={{ display: "flex", gap: 12 }}>
      <span
        style={{
          width: 90,
          font: "11px var(--sans)",
          color: "var(--fg-muted)",
          flex: "none",
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
