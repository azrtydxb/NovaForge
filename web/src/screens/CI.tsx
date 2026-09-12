import { useState } from "react";
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
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
import { Dialog, NewButton } from "../components/Dialog";
import type { Artifact, CIJob, CIRun } from "../lib/types";

/** CI is the design's run list plus a live log. The log polls rather than
 * streams: the edge exposes a job's log as a read, and a poll that is honest
 * about being a poll is better than a stream that silently stops. */
export function CI() {
  const w = useWorkspace();
  const repos = scopedRepos(w);
  const [selected, setSelected] = useState<{ repo: string; id: string } | null>(
    null,
  );
  const [triggering, setTriggering] = useState(false);
  const qc = useQueryClient();

  const trigger = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post(`/api/v1/orgs/${enc(w.org!)}/repos/${enc(v.repo!)}/ci/runs`, {
        ref: v.ref,
      }),
    onSuccess: () => {
      setTriggering(false);
      qc.invalidateQueries();
    },
  });

  const queries = useQueries({
    queries: repos.map((r) => ({
      queryKey: ["ci-runs", w.org, r.name],
      queryFn: () =>
        api.get<{ runs: CIRun[] }>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(r.name)}/ci/runs`,
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

  const current =
    selected ?? (rows[0] ? { repo: rows[0].repo, id: rows[0].run.id } : null);

  return (
    <Page
      title="CI"
      subtitle="Every job runs in its own Kubernetes pod"
      actions={
        repos.length > 0 ? (
          <NewButton label="Run CI" onClick={() => setTriggering(true)} />
        ) : null
      }
    >
      {triggering ? (
        <Dialog
          title="Run CI"
          submitLabel="Run"
          fields={[
            {
              name: "repo",
              label: "Repository",
              type: "select",
              options: repos.map((r) => r.name),
              required: true,
            },
            {
              name: "ref",
              label: "Ref",
              placeholder: "leave empty for the default branch",
              help: "The repository's own .novaforge/workflow.yaml at this ref is what runs.",
            },
          ]}
          busy={trigger.isPending}
          error={trigger.error}
          onSubmit={(v) => trigger.mutate(v)}
          onClose={() => setTriggering(false)}
        />
      ) : null}
      {trigger.error ? (
        <div style={{ marginBottom: 14 }}>
          <Failed error={trigger.error} />
        </div>
      ) : null}
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "minmax(0,360px) minmax(0,1fr)",
          gap: 14,
        }}
      >
        <Panel>
          <PanelHead>RUNS</PanelHead>
          {loading ? (
            <Loading />
          ) : rows.length === 0 ? (
            <Empty>
              No CI runs in this scope.
              <br />A push to a repository with a{" "}
              <code style={{ font: "11px var(--mono)" }}>
                .novaforge/workflow.yaml
              </code>{" "}
              schedules one.
            </Empty>
          ) : (
            rows.map(({ run, repo }) => {
              const active = current?.id === run.id;
              return (
                <button
                  key={run.id}
                  onClick={() => setSelected({ repo, id: run.id })}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: 10,
                    width: "100%",
                    padding: "10px 14px",
                    border: "none",
                    borderBottom: "1px solid var(--line)",
                    background: active ? "rgba(77,127,255,.1)" : "transparent",
                    cursor: "pointer",
                    textAlign: "left",
                  }}
                >
                  <span
                    style={{
                      width: 7,
                      height: 7,
                      borderRadius: 99,
                      flex: "none",
                      background:
                        run.status === "success"
                          ? "var(--ok)"
                          : run.status === "failure"
                            ? "var(--bad)"
                            : "var(--fg-muted)",
                      animation:
                        run.status === "running"
                          ? "nfpulse 1.6s infinite"
                          : "none",
                    }}
                  />
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ font: "13px var(--sans)" }}>
                      {run.ref.replace("refs/heads/", "")}
                    </div>
                    <div
                      style={{
                        font: "11px var(--mono)",
                        color: "var(--fg-faint)",
                        marginTop: 2,
                      }}
                    >
                      {run.commit_sha.slice(0, 8)} · {repo}
                    </div>
                  </span>
                  <StatePill state={run.status} />
                </button>
              );
            })
          )}
        </Panel>

        {current ? (
          <RunDetail org={w.org!} repo={current.repo} runId={current.id} />
        ) : (
          <Panel>
            <Empty>Select a run.</Empty>
          </Panel>
        )}
      </div>
    </Page>
  );
}

function RunDetail({
  org,
  repo,
  runId,
}: {
  org: string;
  repo: string;
  runId: string;
}) {
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}/ci`;

  const run = useQuery({
    queryKey: ["ci-run", org, repo, runId],
    queryFn: () =>
      api.get<{ run: CIRun; jobs: CIJob[] }>(`${base}/runs/${enc(runId)}`),
  });

  const firstJob = run.data?.jobs[0];

  const logs = useQuery({
    queryKey: ["ci-log", org, repo, firstJob?.id],
    queryFn: () =>
      api.get<{ lines: string[] }>(`${base}/jobs/${enc(firstJob!.id)}/logs`),
    enabled: firstJob !== undefined,
    // A running job's log grows; a finished job's does not change.
    refetchInterval: run.data?.run.status === "running" ? 3_000 : false,
  });

  const artifacts = useQuery({
    queryKey: ["ci-artifacts", org, repo, firstJob?.id],
    queryFn: () =>
      api.get<{ artifacts: Artifact[] }>(
        `${base}/jobs/${enc(firstJob!.id)}/artifacts`,
      ),
    enabled: firstJob !== undefined,
  });

  return (
    <div
      style={{ display: "flex", flexDirection: "column", gap: 14, minWidth: 0 }}
    >
      <Panel>
        <PanelHead>JOBS</PanelHead>
        <Async query={run}>
          {(d) =>
            d.jobs.length === 0 ? (
              <Empty>This run declared no jobs.</Empty>
            ) : (
              <>
                {d.jobs.map((j) => (
                  <div
                    key={j.id}
                    style={{
                      display: "flex",
                      alignItems: "center",
                      gap: 11,
                      padding: "9px 14px",
                      borderBottom: "1px solid var(--line)",
                    }}
                  >
                    <span style={{ flex: 1, font: "13px var(--sans)" }}>
                      {j.name}
                    </span>
                    <span
                      style={{
                        font: "11px var(--mono)",
                        color: "var(--fg-faint)",
                        maxWidth: 260,
                        overflow: "hidden",
                        textOverflow: "ellipsis",
                        whiteSpace: "nowrap",
                      }}
                    >
                      {j.detail}
                    </span>
                    <StatePill state={j.status} />
                  </div>
                ))}
              </>
            )
          }
        </Async>
      </Panel>

      <Panel style={{ minWidth: 0 }}>
        <PanelHead>
          LOG
          {run.data?.run.status === "running" ? (
            <span
              style={{
                font: "10px var(--mono)",
                color: "var(--ok)",
                animation: "nfpulse 1.6s infinite",
              }}
            >
              live
            </span>
          ) : null}
        </PanelHead>
        {!firstJob ? (
          <Empty>No job to read a log from.</Empty>
        ) : (
          <Async query={logs}>
            {(d) =>
              d.lines.length === 0 ? (
                <Empty>This job has produced no output yet.</Empty>
              ) : (
                <pre
                  style={{
                    margin: 0,
                    padding: 14,
                    maxHeight: 340,
                    overflow: "auto",
                    font: "12px/1.6 var(--mono)",
                    color: "var(--fg-dim)",
                  }}
                >
                  {d.lines.join("\n")}
                </pre>
              )
            }
          </Async>
        )}
      </Panel>

      <Panel>
        <PanelHead>ARTIFACTS</PanelHead>
        {!firstJob ? (
          <Empty>—</Empty>
        ) : (
          <Async query={artifacts}>
            {(d) =>
              d.artifacts.length === 0 ? (
                <Empty>This job declared no artifacts.</Empty>
              ) : (
                <>
                  {d.artifacts.map((a) => (
                    <div
                      key={a.id}
                      style={{
                        display: "flex",
                        gap: 11,
                        padding: "9px 14px",
                        borderBottom: "1px solid var(--line)",
                      }}
                    >
                      <span style={{ flex: 1, font: "12px var(--mono)" }}>
                        {a.name}
                      </span>
                      <span
                        style={{
                          font: "11px var(--mono)",
                          color: "var(--fg-faint)",
                        }}
                      >
                        {bytes(a.size_bytes)}
                      </span>
                    </div>
                  ))}
                </>
              )
            }
          </Async>
        )}
      </Panel>
    </div>
  );
}

function bytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(0)} KB`;
  return `${n} B`;
}
