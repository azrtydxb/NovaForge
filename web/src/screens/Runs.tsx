import { useQueries } from "@tanstack/react-query";
import { Link } from "react-router-dom";
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
import type { EngineeringRun } from "../lib/types";

/** Runs lists Engineering Runs — the platform's pull request, which carries
 * plan and proof rather than only a diff. */
export function Runs() {
  const w = useWorkspace();
  const repos = scopedRepos(w);

  const queries = useQueries({
    queries: repos.map((r) => ({
      queryKey: ["runs", w.org, r.name],
      queryFn: () =>
        api.get<{ runs: EngineeringRun[] }>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(r.name)}/runs`,
        ),
      enabled: w.org !== null,
    })),
  });

  const loading = queries.some((q) => q.isLoading);
  const rows = queries
    .flatMap((q, i) =>
      (q.data?.runs ?? []).map((run) => ({ run, repo: repos[i]!.name })),
    )
    .sort((a, b) => b.run.created_at.localeCompare(a.run.created_at));

  return (
    <Page
      title="Engineering Runs"
      subtitle="Every run presents its plan, its change impact, and its per-gate proof"
    >
      <Panel>
        <PanelHead>
          <span style={{ width: 60 }}>RUN</span>
          <span style={{ flex: 1 }}>TITLE</span>
          <span style={{ width: 170 }}>BRANCH</span>
          <span style={{ width: 130 }}>AUTHOR</span>
          <span style={{ width: 110 }}>REPOSITORY</span>
          <span style={{ width: 80 }}>STATE</span>
        </PanelHead>
        {loading ? (
          <Loading />
        ) : rows.length === 0 ? (
          <Empty>
            No Engineering Runs in this scope.
            <br />
            An agent opens one when it finishes work; a person opens one with{" "}
            <code style={{ font: "11px var(--mono)" }}>nf run create</code>.
          </Empty>
        ) : (
          rows.map(({ run, repo }) => (
            <Row key={run.id}>
              <Link
                to={`/runs/${enc(repo)}/${run.number}`}
                style={{ width: 60, font: "600 12px var(--mono)" }}
              >
                #{run.number}
              </Link>
              <span style={{ flex: 1, font: "13px var(--sans)" }}>
                {run.title}
              </span>
              <span
                style={{
                  width: 170,
                  font: "11px var(--mono)",
                  color: "var(--fg-dim)",
                  overflow: "hidden",
                  textOverflow: "ellipsis",
                }}
              >
                {run.source_ref} → {run.target_ref}
              </span>
              <span
                style={{
                  width: 130,
                  font: "11px var(--mono)",
                  color: "var(--fg-muted)",
                }}
              >
                {run.author_kind === "agent"
                  ? run.agent_name || "agent"
                  : "human"}
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
              <span style={{ width: 80 }}>
                <StatePill state={run.state} />
              </span>
            </Row>
          ))
        )}
      </Panel>
    </Page>
  );
}
