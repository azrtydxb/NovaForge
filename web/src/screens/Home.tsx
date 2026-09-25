import { useQueries, useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api, enc } from "../lib/api";
import { scopedRepos, useWorkspace } from "../lib/workspace";
import {
  Async,
  Empty,
  Failed,
  Loading,
  Page,
  Panel,
  PanelHead,
  StatePill,
} from "../components/ui";
import type { Dashboard, EngineeringRun, WorkItem } from "../lib/types";

/** Home is the design's overview: the counts that need a person, what is
 * open, and what the platform has been doing. Every number is the platform's
 * own — nothing here is computed twice in two places. */
export function Home() {
  const w = useWorkspace();
  const repos = scopedRepos(w);

  const dash = useQuery({
    queryKey: ["dashboard", w.org],
    queryFn: () => api.get<Dashboard>(`/api/v1/orgs/${enc(w.org!)}/dashboard`),
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
    })),
  });

  const runs = useQueries({
    queries: repos.map((r) => ({
      queryKey: ["runs", w.org, r.name],
      queryFn: () =>
        api.get<{ runs: EngineeringRun[] }>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(r.name)}/runs`,
        ),
      enabled: w.org !== null,
    })),
  });

  const openWork = work
    .flatMap((q, i) =>
      (q.data?.items ?? []).map((it) => ({ item: it, repo: repos[i]!.name })),
    )
    .filter((x) => x.item.state !== "done")
    .slice(0, 8);

  const openRuns = runs
    .flatMap((q, i) =>
      (q.data?.runs ?? []).map((r) => ({ run: r, repo: repos[i]!.name })),
    )
    .filter((x) => x.run.state === "open")
    .slice(0, 6);

  return (
    <Page
      title="Home"
      subtitle={`${w.org ?? ""}${w.repo ? ` / ${w.repo}` : " — all projects"}`}
    >
      <div
        style={{
          font: "12px var(--sans)",
          color: "var(--fg-muted)",
          marginBottom: 8,
        }}
      >
        Organization-wide totals · {w.org}
      </div>
      <Async query={dash}>
        {(d) => (
          <div
            style={{
              display: "grid",
              gridTemplateColumns: "repeat(auto-fit,minmax(150px,1fr))",
              gap: 10,
              marginBottom: 18,
            }}
          >
            <Stat label="Agents running" value={d.agents_running} tone="ok" />
            <Stat label="Agents blocked" value={d.agents_blocked} tone="bad" />
            <Stat
              label="Need human review"
              value={d.need_human_review}
              tone="warn"
            />
            <Stat
              label="Architecture decisions"
              value={d.architecture_decisions}
              tone="warn"
            />
            <Stat label="Gate failures" value={d.gate_failures} tone="bad" />
            <Stat
              label="Ready to auto-merge"
              value={d.ready_to_auto_merge}
              tone="info"
            />
          </div>
        )}
      </Async>

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "minmax(0,1.2fr) minmax(0,1fr)",
          gap: 14,
        }}
      >
        <Panel>
          <PanelHead>
            OPEN WORK
            <div style={{ flex: 1 }} />
            <Link to="/work" style={{ font: "11px var(--sans)" }}>
              all work →
            </Link>
          </PanelHead>
          {work.some((q) => q.isLoading) ? (
            <Loading />
          ) : work.some((q) => q.error) ? (
            <Failed error={work.find((q) => q.error)!.error} />
          ) : openWork.length === 0 ? (
            <Empty>
              No open Work Items in this scope.
              <br />
              Create one from the Work screen, or let a maintenance scan propose
              some.
            </Empty>
          ) : (
            openWork.map(({ item, repo }) => (
              <Row key={item.id}>
                <Link
                  to={`/work/${enc(repo)}/${enc(item.key)}`}
                  style={{ font: "600 12px var(--mono)" }}
                >
                  {item.key}
                </Link>
                <span style={{ flex: 1, font: "13px var(--sans)" }}>
                  {item.goal}
                </span>
                <span
                  style={{ font: "11px var(--mono)", color: "var(--fg-faint)" }}
                >
                  {repo}
                </span>
                <StatePill state={item.state} />
              </Row>
            ))
          )}
        </Panel>

        <Panel>
          <PanelHead>
            OPEN ENGINEERING RUNS
            <div style={{ flex: 1 }} />
            <Link to="/runs" style={{ font: "11px var(--sans)" }}>
              all runs →
            </Link>
          </PanelHead>
          {runs.some((q) => q.isLoading) ? (
            <Loading />
          ) : runs.some((q) => q.error) ? (
            <Failed error={runs.find((q) => q.error)!.error} />
          ) : openRuns.length === 0 ? (
            <Empty>No open Engineering Runs in this scope.</Empty>
          ) : (
            openRuns.map(({ run, repo }) => (
              <Row key={run.id}>
                <Link
                  to={`/runs/${enc(repo)}/${run.number}`}
                  style={{ font: "600 12px var(--mono)" }}
                >
                  #{run.number}
                </Link>
                <span style={{ flex: 1, font: "13px var(--sans)" }}>
                  {run.title}
                </span>
                <span
                  style={{ font: "11px var(--mono)", color: "var(--fg-faint)" }}
                >
                  {run.author_kind === "agent"
                    ? run.agent_name || "agent"
                    : "human"}
                </span>
              </Row>
            ))
          )}
        </Panel>
      </div>
    </Page>
  );
}

const TONE: Record<string, string> = {
  ok: "var(--ok)",
  bad: "var(--bad)",
  warn: "var(--warn)",
  info: "var(--link)",
};

function Stat({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone: keyof typeof TONE;
}) {
  return (
    <Panel style={{ padding: "13px 14px" }}>
      <div
        style={{
          font: "600 22px var(--sans)",
          color: value > 0 ? TONE[tone] : "var(--fg-muted)",
        }}
      >
        {value}
      </div>
      <div
        style={{
          font: "11px var(--sans)",
          color: "var(--fg-muted)",
          marginTop: 3,
        }}
      >
        {label}
      </div>
    </Panel>
  );
}

export function Row({ children }: { children: React.ReactNode }) {
  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        gap: 11,
        padding: "9px 14px",
        borderBottom: "1px solid var(--line)",
      }}
    >
      {children}
    </div>
  );
}
