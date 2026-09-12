import { useQueries, useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api, enc } from "../lib/api";
import { scopedRepos, useWorkspace } from "../lib/workspace";
import { Empty, Loading, Page, Panel, PanelHead } from "../components/ui";
import type { Dashboard, EngineeringRun, WorkItem } from "../lib/types";

/** Exceptions is the design's "what needs a person" list. Each entry is
 * derived from real state — a blocked Work Item, a run whose gates are green
 * and which policy will not merge on its own — rather than from a separate
 * notification store that could drift from what is actually true. */
export function Exceptions() {
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

  const loading =
    work.some((q) => q.isLoading) || runs.some((q) => q.isLoading);

  const blocked = work.flatMap((q) =>
    (q.data?.items ?? [])
      .filter((it) => it.state === "blocked")
      .map((it) => ({
        kind: "BLOCKED",
        tone: "var(--bad)",
        title: it.goal,
        detail: `${it.key} · a person has to unblock this before its agent can continue`,
        to: "/work",
        cta: "Open work",
      })),
  );

  const review = work.flatMap((q) =>
    (q.data?.items ?? [])
      .filter((it) => it.state === "review")
      .map((it) => ({
        kind: "REVIEW",
        tone: "var(--warn)",
        title: it.goal,
        detail: `${it.key} · waiting on review`,
        to: "/work",
        cta: "Open work",
      })),
  );

  const open = runs.flatMap((q, i) =>
    (q.data?.runs ?? [])
      .filter((r) => r.state === "open")
      .map((r) => ({
        kind: "MERGE",
        tone: "var(--link)",
        title: r.title,
        detail: `#${r.number} in ${repos[i]!.name} · open Engineering Run`,
        to: `/runs/${enc(repos[i]!.name)}/${r.number}`,
        cta: "Inspect",
      })),
  );

  const all = [...blocked, ...review, ...open];

  return (
    <Page
      title="Exceptions"
      subtitle="The platform runs itself until something needs a decision only a person can make"
    >
      {dash.data ? (
        <div
          style={{
            display: "flex",
            gap: 16,
            marginBottom: 14,
            font: "12px var(--sans)",
            color: "var(--fg-muted)",
          }}
        >
          <span>agents blocked: {dash.data.agents_blocked}</span>
          <span>gate failures: {dash.data.gate_failures}</span>
          <span>need review: {dash.data.need_human_review}</span>
          <span>
            architecture decisions: {dash.data.architecture_decisions}
          </span>
        </div>
      ) : null}

      <Panel>
        <PanelHead>NEEDS A PERSON</PanelHead>
        {loading ? (
          <Loading />
        ) : all.length === 0 ? (
          <Empty>
            Nothing needs you right now.
            <br />
            Blocked work, reviews, and open runs appear here.
          </Empty>
        ) : (
          all.map((e, i) => (
            <div
              key={i}
              style={{
                display: "flex",
                alignItems: "center",
                gap: 12,
                padding: "12px 14px",
                borderBottom: "1px solid var(--line)",
              }}
            >
              <span
                style={{
                  width: 8,
                  height: 8,
                  borderRadius: 99,
                  background: e.tone,
                  flex: "none",
                }}
              />
              <span style={{ flex: 1 }}>
                <div style={{ font: "13px var(--sans)" }}>{e.title}</div>
                <div
                  style={{
                    font: "11px var(--mono)",
                    color: "var(--fg-muted)",
                    marginTop: 3,
                  }}
                >
                  {e.detail}
                </div>
              </span>
              <span
                style={{
                  font: "600 9px var(--sans)",
                  letterSpacing: ".1em",
                  color: e.tone,
                }}
              >
                {e.kind}
              </span>
              <Link
                to={e.to}
                style={{
                  padding: "5px 11px",
                  border: "1px solid var(--line-2)",
                  borderRadius: 7,
                  font: "12px var(--sans)",
                }}
              >
                {e.cta}
              </Link>
            </div>
          ))
        )}
      </Panel>
    </Page>
  );
}
