import { useQueries } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { scopedRepos, useWorkspace } from "../lib/workspace";
import {
  Empty,
  Failed,
  Loading,
  Page,
  Panel,
  PanelHead,
  Pill,
} from "../components/ui";

interface Proposal {
  fingerprint: string;
  work_item_key: string;
  work_item_goal: string;
  work_item_type: string;
  state: string;
  resolved: boolean;
}

const TYPE_TONE: Record<string, string> = {
  security: "var(--bad)",
  upgrade: "var(--warn)",
  tech_debt: "var(--fg-muted)",
  bug: "var(--warn)",
  documentation: "var(--fg-muted)",
  refactor: "var(--violet)",
};

/** Maintenance lists what the scanners found and proposed. A proposal is a
 * plain, unassigned Work Item — nothing here has executed a fix, which is the
 * whole point: the platform proposes, a person disposes. */
export function Maintenance() {
  const w = useWorkspace();
  const repos = scopedRepos(w);

  const queries = useQueries({
    queries: repos.map((r) => ({
      queryKey: ["maintenance", w.org, r.name],
      queryFn: () =>
        api.get<{ proposals: Proposal[] }>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(r.name)}/maintenance`,
        ),
      enabled: w.org !== null,
    })),
  });

  const loading = queries.some((q) => q.isLoading);
  const firstError = queries.find((q) => q.error)?.error;
  const rows = queries.flatMap((q, i) =>
    (q.data?.proposals ?? []).map((p) => ({ p, repo: repos[i]!.name })),
  );

  return (
    <Page
      title="Maintenance"
      subtitle="Outdated dependencies, CVEs, flaky tests, dead code, coverage, docs drift, performance and architecture — each proposed as work, never executed"
    >
      <Panel>
        <PanelHead>
          PROPOSALS
          <span style={{ color: "var(--fg-faint)" }}>{rows.length}</span>
        </PanelHead>
        {loading ? (
          <Loading />
        ) : firstError ? (
          <div style={{ padding: 14 }}>
            <Failed error={firstError} />
          </div>
        ) : rows.length === 0 ? (
          <Empty>
            The scanners have proposed nothing in this scope.
            <br />
            They sweep on the interval the chart sets (
            <code style={{ font: "11px var(--mono)" }}>
              factory.maintenance.intervalHours
            </code>
            ).
          </Empty>
        ) : (
          rows.map(({ p, repo }) => (
            <div
              key={p.fingerprint}
              style={{
                display: "flex",
                alignItems: "center",
                gap: 12,
                padding: "11px 14px",
                borderBottom: "1px solid var(--line)",
                opacity: p.resolved ? 0.55 : 1,
              }}
            >
              <Pill
                bg={`${TYPE_TONE[p.work_item_type] ?? "var(--fg-muted)"}22`}
                fg={TYPE_TONE[p.work_item_type] ?? "var(--fg-muted)"}
              >
                {p.work_item_type}
              </Pill>
              <span style={{ flex: 1 }}>
                <div style={{ font: "13px var(--sans)" }}>
                  {p.work_item_goal}
                </div>
                <div
                  style={{
                    font: "11px var(--mono)",
                    color: "var(--fg-faint)",
                    marginTop: 3,
                  }}
                >
                  {p.work_item_key} · {repo}
                  {p.resolved ? " · no longer reproducing" : ""}
                </div>
              </span>
              <span
                style={{ font: "11px var(--mono)", color: "var(--fg-muted)" }}
              >
                {p.state}
              </span>
            </div>
          ))
        )}
      </Panel>
    </Page>
  );
}
