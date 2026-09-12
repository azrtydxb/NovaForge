import { useState } from "react";
import { useQueries } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { scopedRepos, useWorkspace } from "../lib/workspace";
import {
  Empty,
  Loading,
  Page,
  Panel,
  PanelHead,
  StatePill,
} from "../components/ui";
import { Row } from "./Home";
import type { WorkItem } from "../lib/types";

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

export function Work() {
  const w = useWorkspace();
  const repos = scopedRepos(w);
  const [filter, setFilter] = useState<string | null>(null);

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
    >
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
        </PanelHead>
        {loading ? (
          <Loading />
        ) : rows.length === 0 ? (
          <Empty>
            {filter === null
              ? "No Work Items in this scope yet."
              : `No Work Items are ${filter.replace(/_/g, " ")}.`}
          </Empty>
        ) : (
          rows.map(({ item, repo }) => (
            <Row key={item.id}>
              <span
                style={{
                  width: 80,
                  font: "600 12px var(--mono)",
                  color: "var(--link)",
                }}
              >
                {item.key}
              </span>
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
            </Row>
          ))
        )}
      </Panel>
    </Page>
  );
}
