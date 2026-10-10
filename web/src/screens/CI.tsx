import { useEffect, useRef, useState, type ReactNode } from "react";
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
  STATE_COLORS,
} from "../components/ui";
import { Dialog, NewButton } from "../components/Dialog";
import { CancelAgentRun } from "../components/CancelAgentRun";
import type { Artifact, CIJob, CIRun } from "../lib/types";
import { timeAgo } from "./Home";

/** CI is the design's run list plus the run's pipeline graph and a live log.
 * The log polls rather than streams: the edge exposes a job's log as a read,
 * and a poll that is honest about being a poll is better than a stream that
 * silently stops. */
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
      title="CI runs"
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
          alignItems: "start",
        }}
      >
        <Panel>
          <PanelHead title="RUNS" count={rows.length} />
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
            // Runs are grouped by the day they were created, the way the
            // design's lists are: a dense row under a labelled section.
            groupByDay(rows).map(([label, group]) => (
              <div key={label}>
                <div
                  style={{
                    padding: "9px 14px 3px",
                    font: "600 9px var(--mono)",
                    letterSpacing: ".1em",
                    color: "var(--fg-faint)",
                  }}
                >
                  {label}
                </div>
                {group.map(({ run, repo }) => {
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
                        padding: "9px 14px",
                        border: "none",
                        borderLeft: `2px solid ${active ? "var(--accent)" : "transparent"}`,
                        borderBottom: "1px solid var(--line)",
                        background: active ? "var(--accent-softer)" : "none",
                        cursor: "pointer",
                        textAlign: "left",
                      }}
                    >
                      <span
                        aria-hidden
                        style={{
                          width: 7,
                          height: 7,
                          borderRadius: 99,
                          flex: "none",
                          background: runDot(run.status),
                          animation:
                            run.status === "running"
                              ? "nfpulse 1.6s infinite"
                              : "none",
                        }}
                      />
                      <span style={{ flex: 1, minWidth: 0 }}>
                        <span
                          style={{
                            display: "flex",
                            gap: 8,
                            alignItems: "baseline",
                          }}
                        >
                          <span
                            style={{
                              font: "600 13px var(--sans)",
                              overflow: "hidden",
                              textOverflow: "ellipsis",
                              whiteSpace: "nowrap",
                            }}
                          >
                            {run.ref.replace("refs/heads/", "")}
                          </span>
                          <span
                            style={{
                              marginLeft: "auto",
                              font: "11px var(--sans)",
                              color: "var(--fg-faint)",
                              flex: "none",
                            }}
                          >
                            {timeAgo(run.created_at)}
                          </span>
                        </span>
                        <span
                          style={{
                            display: "block",
                            font: "11px var(--mono)",
                            color: "var(--fg-faint)",
                            marginTop: 2,
                          }}
                        >
                          {run.commit_sha.slice(0, 8)} · {repo}
                        </span>
                      </span>
                      <StatePill state={run.status} />
                    </button>
                  );
                })}
              </div>
            ))
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

/** groupByDay buckets sorted (newest first) rows into day-labelled groups,
 * preserving the order of both. */
function groupByDay<T extends { run: CIRun }>(rows: T[]): [string, T[]][] {
  const groups: [string, T[]][] = [];
  let currentLabel: string | null = null;
  for (const row of rows) {
    const label = dayLabel(row.run.created_at);
    if (label !== currentLabel) {
      groups.push([label, [row]]);
      currentLabel = label;
    } else {
      groups[groups.length - 1]![1].push(row);
    }
  }
  return groups;
}

function dayLabel(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "EARLIER";
  const startOf = (x: Date) =>
    new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const days = Math.round((startOf(new Date()) - startOf(d)) / 86_400_000);
  if (days === 0) return "TODAY";
  if (days === 1) return "YESTERDAY";
  return d
    .toLocaleDateString(undefined, { month: "short", day: "numeric" })
    .toUpperCase();
}

/** runDot maps a run status onto its dot colour, the one place this screen
 * decides it, so a list of dots reads as one scale. */
function runDot(status: string): string {
  const [, fg] = STATE_COLORS[status] ?? ["", "var(--fg-muted)"];
  return fg;
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

  // Job counts come from the run's own jobs — they are derived from data the
  // platform sent, never summarised from what a status alone implies.
  const tally = jobs.reduce<Record<string, number>>((m, j) => {
    m[j.status] = (m[j.status] ?? 0) + 1;
    return m;
  }, {});
  const summary = Object.entries(tally)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([status, n]) => `${n} ${status}`)
    .join(" · ");

  return (
    <div
      style={{ display: "flex", flexDirection: "column", gap: 14, minWidth: 0 }}
    >
      <Panel>
        {run.isLoading ? (
          <Loading />
        ) : run.error ? (
          <Failed error={run.error} />
        ) : run.data ? (
          <div
            style={{
              display: "flex",
              alignItems: "baseline",
              gap: 10,
              flexWrap: "wrap",
              padding: "13px 14px",
            }}
          >
            <span style={{ font: "600 15px var(--sans)" }}>
              {run.data.run.ref.replace("refs/heads/", "")}
            </span>
            <span style={{ font: "12px var(--mono)", color: "var(--fg-dim)" }}>
              {run.data.run.commit_sha.slice(0, 8)}
            </span>
            <span
              style={{ font: "11px var(--mono)", color: "var(--fg-faint)" }}
            >
              {repo}
            </span>
            <span
              style={{ font: "11px var(--sans)", color: "var(--fg-faint)" }}
            >
              {timeAgo(run.data.run.created_at)}
            </span>
            <span style={{ flex: 1 }} />
            {summary ? (
              <span
                style={{ font: "11px var(--mono)", color: "var(--fg-muted)" }}
              >
                {jobs.length} {jobs.length === 1 ? "job" : "jobs"} · {summary}
              </span>
            ) : null}
            <StatePill state={run.data.run.status} />
          </div>
        ) : null}
      </Panel>

      <Panel>
        <PanelHead title="PIPELINE" count={jobs.length}>
          <span
            title="Jobs are drawn as the workflow declared them. The run's
            response does not carry the needs edges between jobs, so no
            dependency arrows are drawn: nodes without invented edges."
            style={{ font: "11px var(--sans)", color: "var(--fg-faint)" }}
          >
            jobs of this run
          </span>
        </PanelHead>
        <Async query={run}>
          {(d) =>
            d.jobs.length === 0 ? (
              <Empty>
                {d.run.status === "queued"
                  ? "This run has no jobs yet."
                  : "This run declared no jobs."}
              </Empty>
            ) : (
              <div
                style={{
                  display: "flex",
                  flexWrap: "wrap",
                  gap: 10,
                  padding: 14,
                }}
              >
                {d.jobs.map((j) => (
                  <PipelineNode
                    key={j.id}
                    job={j}
                    repo={repo}
                    org={org}
                    selected={selectedJob?.id === j.id}
                    onPick={() => setPickedJob(j.id)}
                  />
                ))}
              </div>
            )
          }
        </Async>
      </Panel>

      <Panel style={{ minWidth: 0 }}>
        <PanelHead title="LOG">
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
        <PanelHead title="ARTIFACTS" />
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

/** PipelineNode is one job drawn as a node of the run's pipeline. The node is
 * a div with the button role rather than a button: an agent job executing as
 * a running Agent Run carries a Cancel control, and a button inside a button
 * is HTML the browser will not forgive. */
function PipelineNode({
  job,
  org,
  repo,
  selected,
  onPick,
}: {
  job: CIJob;
  org: string;
  repo: string;
  selected: boolean;
  onPick: () => void;
}) {
  const [, fg] = STATE_COLORS[job.status] ?? ["", "var(--fg-muted)"];
  const live = job.status === "running" || job.status === "pending";
  const badges: ReactNode[] = [];
  if (job.agent_role)
    badges.push(
      <span
        key="agent"
        style={{
          font: "600 9px var(--mono)",
          letterSpacing: ".06em",
          color: "var(--accent)",
        }}
      >
        AGENT · {job.agent_role.toUpperCase()}
      </span>,
    );
  if (job.work_item_key)
    badges.push(
      <Link
        key="wi"
        to={`/work/${enc(repo)}/${enc(job.work_item_key)}`}
        style={{ font: "11px var(--mono)" }}
      >
        {job.work_item_key}
      </Link>,
    );

  return (
    <div
      role="button"
      tabIndex={0}
      aria-pressed={selected}
      title="Show this job's log and artifacts"
      onClick={onPick}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onPick();
        }
      }}
      style={{
        minWidth: 172,
        background: "var(--panel-2)",
        border: `1px solid ${selected ? "var(--accent)" : "var(--line-2)"}`,
        borderRadius: 8,
        cursor: "pointer",
        overflow: "hidden",
      }}
    >
      <div style={{ height: 2, background: fg, opacity: live ? 1 : 0.55 }} />
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 8,
          padding: "9px 11px 3px",
        }}
      >
        <span
          aria-hidden
          style={{
            width: 7,
            height: 7,
            borderRadius: 99,
            flex: "none",
            background: fg,
            animation: live ? "nfpulse 1.6s infinite" : "none",
          }}
        />
        <span
          style={{
            font: "600 12px var(--sans)",
            overflow: "hidden",
            textOverflow: "ellipsis",
            whiteSpace: "nowrap",
          }}
        >
          {job.name}
        </span>
        <span style={{ flex: 1 }} />
        <StatePill state={job.status} />
      </div>
      {job.detail ? (
        <div
          style={{
            padding: "0 11px",
            font: "11px var(--mono)",
            color: "var(--fg-faint)",
            overflow: "hidden",
            textOverflow: "ellipsis",
            whiteSpace: "nowrap",
          }}
        >
          {job.detail}
        </div>
      ) : null}
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 8,
          padding: "6px 11px 10px",
          flexWrap: "wrap",
        }}
      >
        {badges}
        {/* An agent job executes as an Agent Run; while the job is running,
            that run is the thing to stop. The CI worker settles the job as
            cancelled once it sees the run's state. */}
        {job.agent_run_id && job.status === "running" ? (
          <span style={{ marginLeft: "auto" }}>
            <CancelAgentRun org={org} runId={job.agent_run_id} />
          </span>
        ) : null}
      </div>
    </div>
  );
}

function bytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(0)} KB`;
  return `${n} B`;
}
