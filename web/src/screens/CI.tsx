import { useEffect, useRef, useState } from "react";
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
import { CancelAgentRun } from "../components/CancelAgentRun";
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
  const listError = queries.find((q) => q.error)?.error;
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
          ) : listError ? (
            <Failed error={listError} />
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
          <RunDetail
            key={current.id}
            org={w.org!}
            repo={current.repo}
            runId={current.id}
          />
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
    // An agent job takes minutes and settles on its own; follow it.
    refetchInterval: (q) =>
      q.state.data?.run.status === "running" ||
      q.state.data?.run.status === "queued"
        ? 5_000
        : false,
  });

  // The log shown is the job the person picked. Until they pick one it is the
  // job most worth watching: the first still running, else the first. It used
  // to be the first job, always — a failing second job's log could not be
  // read from this screen at all.
  const [pickedJob, setPickedJob] = useState<string | null>(null);
  const jobs = run.data?.jobs ?? [];
  const selectedJob =
    jobs.find((j) => j.id === pickedJob) ??
    jobs.find((j) => j.status === "running") ??
    jobs[0];
  const jobLive =
    selectedJob?.status === "running" || selectedJob?.status === "pending";

  const logs = useQuery({
    queryKey: ["ci-log", org, repo, selectedJob?.id, selectedJob?.status],
    queryFn: () =>
      api.get<{ lines: string[] }>(`${base}/jobs/${enc(selectedJob!.id)}/logs`),
    enabled: selectedJob !== undefined,
    // A running job's log grows; a finished job's does not change.
    refetchInterval: jobLive ? 2_000 : false,
  });

  const artifacts = useQuery({
    queryKey: ["ci-artifacts", org, repo, selectedJob?.id, selectedJob?.status],
    queryFn: () =>
      api.get<{ artifacts: Artifact[] }>(
        `${base}/jobs/${enc(selectedJob!.id)}/artifacts`,
      ),
    enabled: selectedJob !== undefined,
  });

  // Following a running job means staying at the end of its log as it grows,
  // unless the person has scrolled up to read something.
  const logBox = useRef<HTMLPreElement>(null);
  const pinned = useRef(true);
  const lineCount = logs.data?.lines.length ?? 0;
  useEffect(() => {
    const el = logBox.current;
    if (el && pinned.current) el.scrollTop = el.scrollHeight;
  }, [lineCount, selectedJob?.id]);

  const [downloadError, setDownloadError] = useState<unknown>(null);
  const download = (a: Artifact) => {
    setDownloadError(null);
    api
      .download(`${base}/artifacts/${enc(a.id)}`, a.name)
      .catch((e: unknown) => setDownloadError(e));
  };

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
                    role="button"
                    tabIndex={0}
                    aria-pressed={selectedJob?.id === j.id}
                    title="Show this job's log and artifacts"
                    onClick={() => setPickedJob(j.id)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === " ") {
                        e.preventDefault();
                        setPickedJob(j.id);
                      }
                    }}
                    style={{
                      display: "flex",
                      alignItems: "center",
                      gap: 11,
                      padding: "9px 14px",
                      borderBottom: "1px solid var(--line)",
                      cursor: "pointer",
                      background:
                        selectedJob?.id === j.id
                          ? "rgba(77,127,255,.1)"
                          : "transparent",
                    }}
                  >
                    <span style={{ flex: 1, font: "13px var(--sans)" }}>
                      {j.name}
                      {j.agent_role ? (
                        <span
                          style={{
                            marginLeft: 8,
                            font: "10px var(--mono)",
                            color: "var(--accent)",
                          }}
                        >
                          agent · {j.agent_role}
                        </span>
                      ) : null}
                      {j.work_item_key ? (
                        <Link
                          to={`/work/${enc(repo)}/${enc(j.work_item_key)}`}
                          style={{
                            marginLeft: 8,
                            font: "11px var(--mono)",
                          }}
                        >
                          {j.work_item_key}
                        </Link>
                      ) : null}
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
                    {/* An agent job executes as an Agent Run; while the job
                        is running, that run is the thing to stop. The CI
                        worker settles the job as cancelled once it sees the
                        run's state. */}
                    {j.agent_run_id && j.status === "running" ? (
                      <CancelAgentRun org={org} runId={j.agent_run_id} />
                    ) : null}
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
          {selectedJob ? (
            <span style={{ font: "11px var(--mono)", color: "var(--fg-dim)" }}>
              {selectedJob.name}
            </span>
          ) : null}
          {jobLive ? (
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
        {!selectedJob ? (
          <Empty>No job to read a log from.</Empty>
        ) : (
          <Async query={logs}>
            {(d) =>
              d.lines.length === 0 ? (
                <Empty>
                  {jobLive
                    ? "This job has produced no output yet."
                    : "This job produced no output."}
                </Empty>
              ) : (
                <pre
                  ref={logBox}
                  onScroll={(e) => {
                    const el = e.currentTarget;
                    pinned.current =
                      el.scrollHeight - el.scrollTop - el.clientHeight < 24;
                  }}
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
        {downloadError ? (
          <div style={{ padding: "9px 14px" }}>
            <Failed error={downloadError} />
          </div>
        ) : null}
        {!selectedJob ? (
          <Empty>—</Empty>
        ) : (
          <Async query={artifacts}>
            {(d) =>
              d.artifacts.length === 0 ? (
                <Empty>
                  {jobLive
                    ? "Artifacts are collected when the job succeeds."
                    : "This job kept no artifacts."}
                </Empty>
              ) : (
                <>
                  {d.artifacts.map((a) => (
                    <div
                      key={a.id}
                      style={{
                        display: "flex",
                        alignItems: "center",
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
                      <button
                        type="button"
                        onClick={() => download(a)}
                        style={{
                          font: "11px var(--mono)",
                          color: "var(--accent)",
                          background: "transparent",
                          border: "1px solid var(--line)",
                          borderRadius: 6,
                          padding: "3px 8px",
                          cursor: "pointer",
                        }}
                      >
                        Download
                      </button>
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
