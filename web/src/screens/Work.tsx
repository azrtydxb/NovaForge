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
 * review, done, blocked). "All" is not a state, it is the absence of a filter. */
const FILTERS: { label: string; state: string | null }[] = [
  { label: "All", state: null },
  { label: "Working", state: "in_progress" },
  { label: "Review", state: "review" },
  { label: "Blocked", state: "blocked" },
  { label: "Planning", state: "planning" },
  { label: "Open", state: "open" },
  { label: "Done", state: "done" },
];

/** lines turns a one-per-line textarea into the list the API takes. */
function lines(v: string | undefined): string[] {
  return (v ?? "")
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);
}

export function Work() {
  const w = useWorkspace();
  const repos = scopedRepos(w);
  const [filter, setFilter] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [starting, setStarting] = useState<{
    repo: string;
    key: string;
  } | null>(null);
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
  const rows = queries
    .flatMap((q, i) =>
      (q.data?.items ?? []).map((item) => ({ item, repo: repos[i]!.name })),
    )
    .filter((x) => filter === null || x.item.state === filter)
    .sort((a, b) => b.item.created_at.localeCompare(a.item.created_at));

  return (
    <Page
      title="Work"
      subtitle="Typed engineering intent: goal, acceptance criteria, constraints, required gates"
      actions={
        repos.length > 0 ? (
          <NewButton label="New Work Item" onClick={() => setCreating(true)} />
        ) : null
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
          return (
            <button
              key={f.label}
              onClick={() => setFilter(f.state)}
              style={{
                padding: "5px 11px",
                borderRadius: 7,
                border: "1px solid var(--line)",
                background: active ? "rgba(77,127,255,.18)" : "transparent",
                color: active ? "#fff" : "var(--fg-muted)",
                font: "500 12px var(--sans)",
                cursor: "pointer",
              }}
            >
              {f.label}
            </button>
          );
        })}
      </div>

      <Panel>
        <PanelHead>
          <span style={{ width: 80 }}>KEY</span>
          <span style={{ flex: 1 }}>GOAL</span>
          <span style={{ width: 110 }}>TYPE</span>
          <span style={{ width: 110 }}>REPOSITORY</span>
          <span style={{ width: 90 }}>ASSIGNEE</span>
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
              <span style={{ flex: 1, font: "13px var(--sans)" }}>
                {item.goal}
              </span>
              <span
                style={{
                  width: 110,
                  font: "11px var(--mono)",
                  color: "var(--fg-dim)",
                }}
              >
                {item.type}
              </span>
              <span
                style={{
                  width: 110,
                  font: "11px var(--mono)",
                  color: "var(--fg-faint)",
                }}
              >
                {repo}
              </span>
              <span
                style={{
                  width: 90,
                  font: "11px var(--mono)",
                  color: "var(--fg-muted)",
                }}
              >
                {item.assignee_kind || "—"}
              </span>
              <span style={{ width: 90 }}>
                <StatePill state={item.state} />
              </span>
              <span style={{ width: 74 }}>
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
    </Page>
  );
}
