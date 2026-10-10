import { useQueries, useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
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
import { timeAgo } from "./Home";
import type { Agent, AgentRun, Subtask, WorkItem } from "../lib/types";

/** Swarm shows an epic's decomposition as the design's waves: what can start
 * now, and what is still waiting on something.
 *
 * The design draws every wave with its number and "depends on" edges, but the
 * platform exposes only its own readiness verdict — /subtasks sends `ready`,
 * and no HTTP endpoint returns the dependency edges behind it. Peeling wave 2
 * out of wave 3 here would mean re-deriving the planner's ordering in a
 * browser from half the facts, so the board shows Wave 1 as the platform's
 * ready set and says plainly that later waves cannot be named in this
 * deployment. A screen that guessed the ordering would look more finished and
 * be wrong exactly when someone relies on it. */
export function Swarm() {
  const w = useWorkspace();
  const repos = scopedRepos(w);

  // Assignee ids on subtasks are agent ids; resolving them to names is what
  // turns "3 items assigned to b3f2…" into "assigned to builder-1".
  const agents = useQuery({
    queryKey: ["agents", w.org],
    queryFn: () =>
      api.get<{ agents: Agent[] }>(`/api/v1/orgs/${enc(w.org!)}/agents`),
    enabled: w.org !== null,
  });

  const work = useQueries({
    queries: repos.map((r) => ({
      queryKey: ["work", w.org, r.name],
      queryFn: () =>
        api.get<{ items: WorkItem[] }>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(r.name)}/work`,
        ),
      enabled: w.org !== null,
      refetchInterval: 10_000,
    })),
  });

  const epics = work.flatMap((q, i) =>
    (q.data?.items ?? [])
      .filter((it) => it.state !== "done")
      .map((item) => ({ item, repo: repos[i]!.name })),
  );

  const subtasks = useQueries({
    queries: epics.map(({ item, repo }) => ({
      queryKey: ["subtasks", w.org, repo, item.key],
      queryFn: () =>
        api.get<{ subtasks: Subtask[] }>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(repo)}/work/${enc(item.key)}/subtasks`,
        ),
      enabled: w.org !== null,
      refetchInterval: 10_000,
    })),
  });

  const loading =
    work.some((q) => q.isLoading) || subtasks.some((q) => q.isLoading);

  const listError = [...work, ...subtasks].find((q) => q.error)?.error;

  const decomposed = epics
    .map((e, i) => ({ ...e, subtasks: subtasks[i]?.data?.subtasks ?? [] }))
    .filter((e) => e.subtasks.length > 0);

  // Spend per subtask comes from the runs actually started against it — the
  // same record the Work Item screen reads. A subtask nobody has started a run
  // against has no spend, which is shown as "—" and is not the same as zero
  // work having happened.
  const runKeys = decomposed.flatMap((e) =>
    e.subtasks.map((s) => ({ repo: e.repo, key: s.key })),
  );
  const runs = useQueries({
    queries: runKeys.map(({ repo, key }) => ({
      queryKey: ["agent-runs", w.org, repo, key],
      queryFn: () =>
        api.get<{ runs: AgentRun[] }>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(repo)}/work/${enc(key)}/agent-runs`,
        ),
      enabled: w.org !== null,
      refetchInterval: 10_000,
    })),
  });
  const runsFor = (repo: string, key: string): AgentRun[] | undefined =>
    runs[runKeys.findIndex((r) => r.repo === repo && r.key === key)]?.data
      ?.runs;
  const anyRunError = runs.some((q) => q.error);

  const nameFor = (id: string): string | undefined =>
    agents.data?.agents.find((a) => a.id === id)?.name;

  return (
    <Page
      title="Swarm"
      subtitle="Epics broken into dependency-ordered subtasks across specialized agents"
    >
      {loading ? (
        <Panel>
          <Loading />
        </Panel>
      ) : listError ? (
        <Failed error={listError} />
      ) : decomposed.length === 0 ? (
        <Panel>
          <Empty>
            Nothing is decomposed in this scope.
            <br />
            Decompose an epic with{" "}
            <code style={{ font: "11px var(--mono)" }}>
              nf work decompose &lt;repo&gt; &lt;key&gt;
            </code>
            .
          </Empty>
        </Panel>
      ) : (
        decomposed.map((e, ei) => {
          const done = e.subtasks.filter((s) => s.state === "done");
          const ready = e.subtasks.filter((s) => s.ready && s.state !== "done");
          const waiting = e.subtasks.filter(
            (s) => !s.ready && s.state !== "done",
          );
          // Spend is summed only over reads that succeeded: a failed read
          // must not silently shrink the total, so the epic says its spend
          // may be incomplete instead.
          const runReads = e.subtasks.map((s) => runsFor(e.repo, s.key));
          const spendKnown = runReads.every((r) => r !== undefined);
          const epicRuns = runReads.flatMap((r) => r ?? []);
          const spend = epicRuns.reduce(
            (sum, r) => sum + r.cost_used_micros,
            0,
          );
          const assigned = new Set(
            e.subtasks
              .filter((s) => s.assignee_kind === "agent")
              .map((s) => s.assignee_id),
          );
          return (
            <div key={e.item.id} style={{ marginBottom: 22 }}>
              <div
                style={{
                  display: "flex",
                  alignItems: "baseline",
                  gap: 10,
                  marginBottom: 8,
                  flexWrap: "wrap",
                }}
              >
                <span
                  style={{
                    font: "600 10px var(--mono)",
                    letterSpacing: ".08em",
                    color: "var(--fg-faint)",
                  }}
                >
                  SWARM · EPIC
                </span>
                <Link
                  to={`/work/${enc(e.repo)}/${enc(e.item.key)}`}
                  style={{ font: "600 13px var(--mono)" }}
                >
                  {e.item.key}
                </Link>
                <span style={{ font: "14px var(--sans)" }}>{e.item.goal}</span>
                <span
                  style={{ font: "11px var(--mono)", color: "var(--fg-faint)" }}
                >
                  {e.repo}
                </span>
                <StatePill state={e.item.state} />
              </div>

              <div
                style={{
                  display: "grid",
                  gridTemplateColumns:
                    "repeat(auto-fit,minmax(180px,220px)) minmax(0,1fr)",
                  gap: 12,
                  marginBottom: 12,
                }}
              >
                <Total
                  label="Progress"
                  value={`${done.length} / ${e.subtasks.length} items`}
                />
                <Total
                  label="Recorded spend"
                  value={
                    spendKnown ? (spend > 0 ? dollars(spend) : "$0.00") : "—"
                  }
                  hint={
                    anyRunError
                      ? "some run reads failed; this may be incomplete"
                      : "summed over the epic's agent runs; a run's spend is recorded when it ends"
                  }
                />
                <Total
                  label="Agents assigned"
                  value={
                    assigned.size > 0
                      ? `${assigned.size} · ${[...assigned]
                          .map((id) => (nameFor(id) ?? id)[0]!.toUpperCase())
                          .join(" ")}`
                      : "—"
                  }
                />
                <Total
                  label="ETA"
                  value="—"
                  hint="not computable in this deployment: wave history is not published"
                />
              </div>

              <div
                style={{
                  display: "grid",
                  gridTemplateColumns: "repeat(auto-fit,minmax(260px,1fr))",
                  gap: 12,
                }}
              >
                <Wave
                  title="Wave 1 · Ready"
                  hint="no unfinished dependencies"
                  items={ready}
                  repo={e.repo}
                  runsFor={(key) => runsFor(e.repo, key)}
                  nameFor={nameFor}
                />
                <Wave
                  title="Waiting"
                  hint="the platform has these blocked; this deployment's API does not expose dependency edges, so what blocks each one cannot be named here"
                  items={waiting}
                  repo={e.repo}
                  runsFor={(key) => runsFor(e.repo, key)}
                  nameFor={nameFor}
                />
                <Wave
                  title="Done"
                  hint=""
                  items={done}
                  repo={e.repo}
                  runsFor={(key) => runsFor(e.repo, key)}
                  nameFor={nameFor}
                />
              </div>

              {epicRuns.length > 0 ? (
                <Panel style={{ marginTop: 12 }}>
                  <PanelHead title="Recent runs" count={epicRuns.length} />
                  {epicRuns
                    .slice()
                    .sort((a, b) => b.started_at.localeCompare(a.started_at))
                    .slice(0, 5)
                    .map((r) => (
                      <div
                        key={r.id}
                        style={{
                          display: "flex",
                          gap: 8,
                          alignItems: "baseline",
                          padding: "8px 14px",
                          borderBottom: "1px solid var(--line)",
                          font: "13px var(--sans)",
                        }}
                      >
                        <b>{nameFor(r.agent_id) ?? r.agent_id}</b>
                        <span style={{ color: "var(--fg-muted)" }}>
                          started a run
                        </span>
                        <Link to={`/agent-runs/${enc(r.id)}`}>
                          {r.id.slice(0, 8)}
                        </Link>
                        <StatePill state={r.state} />
                        <span
                          style={{
                            marginLeft: "auto",
                            font: "11px var(--sans)",
                            color: "var(--fg-faint)",
                          }}
                        >
                          {timeAgo(r.started_at)}
                        </span>
                      </div>
                    ))}
                </Panel>
              ) : null}

              {ei === decomposed.length - 1 ? (
                <div
                  style={{
                    font: "11px var(--sans)",
                    color: "var(--fg-faint)",
                    marginTop: 10,
                  }}
                >
                  Re-plan, pause and manual wave starts have no endpoint in this
                  deployment; agents pick up ready items as the scheduler claims
                  them.
                </div>
              ) : null}
            </div>
          );
        })
      )}
    </Page>
  );
}

function Wave({
  title,
  hint,
  items,
  repo,
  runsFor,
  nameFor,
}: {
  title: string;
  hint: string;
  items: Subtask[];
  repo: string;
  runsFor: (key: string) => AgentRun[] | undefined;
  nameFor: (id: string) => string | undefined;
}) {
  return (
    <Panel>
      <PanelHead title={title} count={items.length} />
      {hint ? (
        <div
          style={{
            padding: "7px 14px",
            font: "10px var(--sans)",
            color: "var(--fg-faint)",
            borderBottom: "1px solid var(--line)",
          }}
        >
          {hint}
        </div>
      ) : null}
      {items.length === 0 ? (
        <Empty>—</Empty>
      ) : (
        items.map((s) => {
          const rs = runsFor(s.key);
          // A run read that failed must not read as "no runs": the cost cell
          // shows a question mark and the epic's spend note already says the
          // total may be incomplete.
          const spend =
            rs === undefined
              ? "?"
              : rs.length === 0
                ? "—"
                : dollars(rs.reduce((sum, r) => sum + r.cost_used_micros, 0));
          const assignee =
            s.assignee_kind === "agent" && s.assignee_id
              ? (nameFor(s.assignee_id) ?? `${s.assignee_id.slice(0, 8)}…`)
              : "";
          return (
            <div
              key={s.id}
              style={{
                padding: "10px 14px",
                borderBottom: "1px solid var(--line)",
              }}
            >
              <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                <Link
                  to={`/work/${enc(repo)}/${enc(s.key)}`}
                  style={{ font: "600 11px var(--mono)" }}
                >
                  {s.key}
                </Link>
                <div style={{ flex: 1 }} />
                <StatePill state={s.state} />
              </div>
              <div style={{ font: "12px var(--sans)", marginTop: 5 }}>
                {s.goal}
              </div>
              <div
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: 8,
                  marginTop: 6,
                }}
              >
                {assignee ? (
                  <>
                    <span
                      style={{
                        width: 18,
                        height: 18,
                        borderRadius: 99,
                        background: "#2f3542",
                        display: "grid",
                        placeItems: "center",
                        font: "600 9px var(--sans)",
                        color: "#fff",
                        flex: "none",
                      }}
                    >
                      {assignee[0]!.toUpperCase()}
                    </span>
                    <span
                      style={{
                        font: "11px var(--sans)",
                        color: "var(--fg-dim)",
                      }}
                    >
                      {assignee}
                    </span>
                  </>
                ) : (
                  <span
                    style={{
                      font: "10px var(--mono)",
                      color: "var(--fg-faint)",
                    }}
                  >
                    {s.assignee_kind || s.type}
                  </span>
                )}
                <div style={{ flex: 1 }} />
                <span
                  title="recorded spend on this subtask's agent runs"
                  style={{
                    font: "11px var(--mono)",
                    color: "var(--fg-muted)",
                  }}
                >
                  {spend}
                </span>
              </div>
            </div>
          );
        })
      )}
    </Panel>
  );
}

function Total({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <div>
      <div
        style={{
          font: "600 10px var(--sans)",
          letterSpacing: ".08em",
          color: "var(--fg-faint)",
        }}
      >
        {label.toUpperCase()}
      </div>
      <div style={{ font: "600 15px var(--sans)", marginTop: 3 }}>{value}</div>
      {hint ? (
        <div
          title={hint}
          style={{
            font: "10px var(--sans)",
            color: "var(--fg-faint)",
            marginTop: 2,
            overflow: "hidden",
            textOverflow: "ellipsis",
            whiteSpace: "nowrap",
          }}
        >
          {hint}
        </div>
      ) : null}
    </div>
  );
}

/** micro-units of cost are 1e-6 of the currency unit; the design reads them
 * as dollars and so does this. */
function dollars(micros: number): string {
  return `$${(micros / 1_000_000).toFixed(2)}`;
}
