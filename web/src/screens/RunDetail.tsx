import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useParams } from "react-router-dom";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
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
import { RunReviews } from "../components/RunReviews";
import type { ApprovalList, EngineeringRun, ProofRecord } from "../lib/types";
import { ApprovalRows } from "../components/Approvals";

interface PlanStep {
  ordinal: number;
  text: string;
  state: string;
}

/** Impact is measured by the platform from the run's diff since its merge
 * base; risk_reasons is the rule that set the risk, shown with it. */
interface Impact {
  files_changed: number;
  insertions: number;
  deletions: number;
  paths: string[];
  risk: string;
  risk_reasons: string[];
}

interface ToolCall {
  tool: string;
  args: string;
  outcome: string;
  error: string;
  started_at: string;
}

const TABS = ["Evidence", "Plan", "Changes", "Tool calls"] as const;
type Tab = (typeof TABS)[number];

export function RunDetail() {
  const { repo = "", number = "" } = useParams();
  const w = useWorkspace();
  const [tab, setTab] = useState<Tab>("Evidence");
  const qc = useQueryClient();

  const base = `/api/v1/orgs/${enc(w.org ?? "")}/repos/${enc(repo)}/runs/${enc(number)}`;

  const run = useQuery({
    queryKey: ["run", w.org, repo, number],
    queryFn: () => api.get<EngineeringRun>(base),
    enabled: w.org !== null,
  });

  const merge = useMutation({
    mutationFn: () => api.post<{ merge_sha: string }>(`${base}/merge`, {}),
    onSuccess: () => qc.invalidateQueries(),
  });

  // Running the gates can take a while — the tests gate runs the suite — so
  // it is its own action, and the Evidence tab shows what it recorded.
  const evaluate = useMutation({
    mutationFn: () => api.post(`${base}/gates/evaluate`, {}),
    onSettled: () => qc.invalidateQueries({ queryKey: ["proof", base] }),
  });

  return (
    <Page
      title={
        run.data ? `#${run.data.number} · ${run.data.title}` : `Run #${number}`
      }
      subtitle={
        run.data
          ? `${run.data.source_ref} → ${run.data.target_ref} · ${
              run.data.author_kind === "agent"
                ? `${run.data.agent_name || "agent"} on ${run.data.model_name || "an unnamed model"}`
                : "authored by a person"
            }`
          : repo
      }
      actions={
        run.data ? (
          <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
            <StatePill state={run.data.state} />
            {run.data.state === "open" ? (
              <button
                onClick={() => evaluate.mutate()}
                disabled={evaluate.isPending}
                style={{
                  padding: "7px 14px",
                  background: "transparent",
                  border: "1px solid var(--line-2)",
                  borderRadius: 8,
                  color: "var(--fg-dim)",
                  font: "12px var(--sans)",
                  cursor: evaluate.isPending ? "wait" : "pointer",
                }}
              >
                {evaluate.isPending ? "Running gates…" : "Run gates"}
              </button>
            ) : null}
            {run.data.state === "open" ? (
              <button
                onClick={() => merge.mutate()}
                disabled={merge.isPending}
                style={{
                  padding: "7px 14px",
                  background: "var(--accent)",
                  border: "none",
                  borderRadius: 8,
                  color: "#fff",
                  font: "600 12px var(--sans)",
                  cursor: "pointer",
                }}
              >
                {merge.isPending ? "Merging…" : "Merge"}
              </button>
            ) : null}
          </div>
        ) : null
      }
    >
      {merge.error ? (
        <div style={{ marginBottom: 14 }}>
          <Failed error={merge.error} />
        </div>
      ) : null}
      {evaluate.error ? (
        <div style={{ marginBottom: 14 }}>
          <Failed error={evaluate.error} />
        </div>
      ) : null}
      {merge.data ? (
        <div
          style={{
            marginBottom: 14,
            padding: "10px 14px",
            border: "1px solid #2bb67344",
            background: "rgba(43,182,115,.07)",
            borderRadius: 9,
            font: "12px var(--mono)",
            color: "var(--ok)",
          }}
        >
          merged as {merge.data.merge_sha}
        </div>
      ) : null}

      {run.error ? <Failed error={run.error} /> : null}
      {w.org && run.data ? (
        <RunReviews base={base} org={w.org} repo={repo} run={run.data} />
      ) : null}
      {w.org !== null ? <RunApprovals org={w.org} base={base} /> : null}

      <div style={{ display: "flex", gap: 6, marginBottom: 12 }}>
        {TABS.map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            style={{
              padding: "6px 13px",
              borderRadius: 7,
              border: "1px solid var(--line)",
              background: tab === t ? "var(--accent-soft)" : "transparent",
              color: tab === t ? "#fff" : "var(--fg-muted)",
              font: "500 12px var(--sans)",
              cursor: "pointer",
            }}
          >
            {t}
          </button>
        ))}
      </div>

      {tab === "Evidence" ? <Evidence base={base} /> : null}
      {tab === "Plan" ? (
        <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
          <ChangeImpact base={base} />
          <Plan base={base} />
        </div>
      ) : null}
      {tab === "Changes" ? (
        <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
          <ChangeImpact base={base} />
          <Changes org={w.org} repo={repo} run={run.data} />
        </div>
      ) : null}
      {tab === "Tool calls" ? <Tools base={base} /> : null}
    </Page>
  );
}

/** RunApprovals shows the decisions this run's change needs from a person.
 * A pending or denied one blocks the merge, and saying so here is what keeps
 * a refused merge from reading as a platform fault. Superseded requests are
 * history: the change moved on and was asked again. Requests appear once the
 * run's gates have been run, or a merge has been attempted. */
function RunApprovals({ org, base }: { org: string; base: string }) {
  const approvals = useQuery({
    queryKey: ["approvals", base],
    queryFn: () => api.get<ApprovalList>(`${base}/approvals`),
  });
  if (approvals.error) {
    return (
      <div style={{ marginBottom: 14 }}>
        <Failed error={approvals.error} />
      </div>
    );
  }
  const list = approvals.data;
  if (!list || list.approvals.length === 0) return null;
  const current = list.approvals.filter((a) => a.decision !== "superseded");
  const blocking = current.filter((a) => a.decision !== "approved").length;
  return (
    <Panel style={{ marginBottom: 14 }}>
      <PanelHead>
        APPROVALS
        <span style={{ color: blocking > 0 ? "var(--warn)" : "var(--ok)" }}>
          {blocking > 0 ? `${blocking} blocking the merge` : "all approved"}
        </span>
      </PanelHead>
      <ApprovalRows
        org={org}
        list={{ ...list, approvals: current }}
        showRun={false}
        emptyText="Every approval this change raised was for an earlier commit."
      />
    </Panel>
  );
}

/** Evidence is the PROOF block: one row per gate, with the detail the gate
 * itself recorded. This is what makes a run reviewable without re-running it. */
function Evidence({ base }: { base: string }) {
  const [selected, setSelected] = useState(0);
  const proof = useQuery({
    queryKey: ["proof", base],
    queryFn: () => api.get<{ proof: ProofRecord[] }>(`${base}/proof`),
  });

  return (
    <Async query={proof}>
      {(d) =>
        d.proof.length === 0 ? (
          <Empty>
            No gate has recorded proof for this run yet.
            <br />
            Merging runs them; so does Run gates.
          </Empty>
        ) : (
          <div
            style={{
              display: "grid",
              gridTemplateColumns: "minmax(0,320px) minmax(0,1fr)",
              gap: 14,
            }}
          >
            <Panel>
              <PanelHead>PROOF AND ASSERTIONS</PanelHead>
              {d.proof.map((p, i) => (
                <button
                  key={p.gate}
                  onClick={() => setSelected(i)}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: 10,
                    width: "100%",
                    padding: "10px 14px",
                    border: "none",
                    borderBottom: "1px solid var(--line)",
                    background:
                      selected === i ? "var(--accent-softer)" : "transparent",
                    cursor: "pointer",
                    textAlign: "left",
                  }}
                >
                  <span style={{ flex: 1, font: "13px var(--sans)" }}>
                    {p.gate}
                    <small style={{ display: "block" }}>{!p.producer ? "Legacy / unverified" : p.producer === "human" ? "Human assertion (not gate evidence)" : `Producer: ${p.producer}`}</small>
                  </span>
                  {p.producer && p.producer !== "human" ? <StatePill state={p.status} /> : <span>Reported: {p.status}</span>}
                </button>
              ))}
            </Panel>
            <Panel>
              <PanelHead>{d.proof[selected]?.gate ?? "PROOF"}</PanelHead>
              <pre
                style={{
                  margin: 0,
                  padding: 16,
                  font: "12px/1.65 var(--mono)",
                  color: "var(--fg-dim)",
                  whiteSpace: "pre-wrap",
                  wordBreak: "break-word",
                }}
              >
                {d.proof[selected]?.detail || "(the gate recorded no detail)"}
              </pre>
            </Panel>
          </div>
        )
      }
    </Async>
  );
}

/** ChangeImpact is the CHANGE IMPACT block: what the run changes, measured by
 * the platform from its diff, and the risk that follows from it — with the
 * rule that set the risk, so it is never a bare label to take on trust. */
function ChangeImpact({ base }: { base: string }) {
  const impact = useQuery({
    queryKey: ["impact", base],
    queryFn: () => api.get<Impact>(`${base}/impact`),
  });
  const riskColor = (risk: string) =>
    risk === "high"
      ? "var(--bad)"
      : risk === "medium"
        ? "var(--warn, #d9a441)"
        : "var(--ok)";

  return (
    <Panel>
      <PanelHead>CHANGE IMPACT</PanelHead>
      <Async query={impact}>
        {(d) => (
          <div style={{ padding: "12px 14px", display: "grid", gap: 10 }}>
            <div
              style={{
                display: "flex",
                gap: 18,
                flexWrap: "wrap",
                alignItems: "baseline",
              }}
            >
              <span style={{ font: "13px var(--sans)" }}>
                <b>{d.files_changed}</b>{" "}
                {d.files_changed === 1 ? "file" : "files"} changed
              </span>
              <span style={{ font: "12px var(--mono)", color: "var(--ok)" }}>
                +{d.insertions}
              </span>
              <span style={{ font: "12px var(--mono)", color: "var(--bad)" }}>
                −{d.deletions}
              </span>
              <span
                style={{
                  font: "600 11px var(--mono)",
                  color: riskColor(d.risk),
                  textTransform: "uppercase",
                }}
              >
                {d.risk} risk
              </span>
            </div>
            {d.risk_reasons.map((r) => (
              <div
                key={r}
                style={{ font: "12px var(--sans)", color: "var(--fg-dim)" }}
              >
                {r}
              </div>
            ))}
            {d.paths.length > 0 ? (
              <ul
                style={{
                  margin: 0,
                  paddingLeft: 18,
                  font: "12px/1.7 var(--mono)",
                  color: "var(--fg-dim)",
                }}
              >
                {d.paths.map((p) => (
                  <li key={p}>{p}</li>
                ))}
              </ul>
            ) : (
              <Empty>The source branch changes nothing.</Empty>
            )}
          </div>
        )}
      </Async>
    </Panel>
  );
}

function Plan({ base }: { base: string }) {
  const plan = useQuery({
    queryKey: ["plan", base],
    queryFn: () => api.get<{ steps: PlanStep[] }>(`${base}/plan`),
  });

  return (
    <Panel>
      <PanelHead>PLAN</PanelHead>
      <Async query={plan}>
        {(d) =>
          d.steps.length === 0 ? (
            <Empty>This run recorded no plan.</Empty>
          ) : (
            <>
              {d.steps.map((s) => (
                <div
                  key={s.ordinal}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: 12,
                    padding: "10px 14px",
                    borderBottom: "1px solid var(--line)",
                  }}
                >
                  <span
                    style={{
                      width: 22,
                      font: "11px var(--mono)",
                      color: "var(--fg-faint)",
                    }}
                  >
                    {s.ordinal}
                  </span>
                  <span style={{ flex: 1, font: "13px var(--sans)" }}>
                    {s.text}
                  </span>
                  <StatePill state={s.state} />
                </div>
              ))}
            </>
          )
        }
      </Async>
    </Panel>
  );
}

/** Changes is the run's diff, read from git-platform between the run's target
 * and source refs — the same two refs the merge would use. */
function Changes({
  org,
  repo,
  run,
}: {
  org: string | null;
  repo: string;
  run: EngineeringRun | undefined;
}) {
  const diff = useQuery({
    queryKey: ["diff", org, repo, run?.source_ref, run?.target_ref],
    queryFn: () =>
      api.get<{ unified: string }>(
        `/api/v1/orgs/${enc(org!)}/repos/${enc(repo)}/diff?from=${enc(
          run!.target_ref,
        )}&to=${enc(run!.source_ref)}&merge_base=true`,
      ),
    enabled: org !== null && run !== undefined,
  });

  if (!run) return <Loading />;

  return (
    <Panel>
      <PanelHead>
        {run.target_ref} → {run.source_ref}
      </PanelHead>
      <Async query={diff}>
        {(d) =>
          !d.unified.trim() ? (
            <Empty>These two refs are identical.</Empty>
          ) : (
            <DiffView unified={d.unified} />
          )
        }
      </Async>
    </Panel>
  );
}

function DiffView({ unified }: { unified: string }) {
  return (
    <div style={{ overflowX: "auto" }}>
      <pre
        style={{ margin: 0, padding: "12px 0", font: "12px/1.6 var(--mono)" }}
      >
        {unified.split("\n").map((line, i) => {
          const add = line.startsWith("+") && !line.startsWith("+++");
          const del = line.startsWith("-") && !line.startsWith("---");
          const hunk = line.startsWith("@@");
          const meta =
            line.startsWith("diff ") ||
            line.startsWith("index ") ||
            line.startsWith("+++") ||
            line.startsWith("---");
          return (
            <div
              key={i}
              style={{
                padding: "0 16px",
                color: add
                  ? "var(--ok)"
                  : del
                    ? "var(--bad)"
                    : hunk
                      ? "var(--link)"
                      : meta
                        ? "var(--fg-faint)"
                        : "var(--fg-dim)",
                background: add
                  ? "#2bb6730e"
                  : del
                    ? "#e5534b0e"
                    : "transparent",
                whiteSpace: "pre",
              }}
            >
              {line || " "}
            </div>
          );
        })}
      </pre>
    </div>
  );
}

/** Tools is the audited record of what the agent actually did. Every tool
 * call an agent makes is recorded with its arguments and its outcome, which
 * is what makes a run's behaviour reviewable rather than described. */
function Tools({ base }: { base: string }) {
  const tools = useQuery({
    queryKey: ["tools", base],
    queryFn: () => api.get<{ calls: ToolCall[] }>(`${base}/tools`),
  });

  return (
    <Panel>
      <PanelHead>
        <span style={{ width: 78 }}>TIME</span>
        <span style={{ width: 190 }}>TOOL</span>
        <span style={{ flex: 1 }}>ARGUMENTS</span>
        <span style={{ width: 90 }}>OUTCOME</span>
      </PanelHead>
      <Async query={tools}>
        {(d) =>
          d.calls.length === 0 ? (
            <Empty>No tool calls were recorded for this run.</Empty>
          ) : (
            <>
              {d.calls.map((c, i) => (
                <div
                  key={`${c.started_at}-${i}`}
                  style={{
                    display: "flex",
                    alignItems: "flex-start",
                    gap: 11,
                    padding: "8px 14px",
                    borderBottom: "1px solid var(--line)",
                  }}
                >
                  <span
                    style={{
                      width: 78,
                      font: "11px var(--mono)",
                      color: "var(--fg-faint)",
                    }}
                  >
                    {c.started_at.slice(11, 19)}
                  </span>
                  <span
                    style={{
                      width: 190,
                      font: "12px var(--mono)",
                      color: "var(--link)",
                    }}
                  >
                    {c.tool}
                  </span>
                  <span
                    style={{
                      flex: 1,
                      font: "11px var(--mono)",
                      color: "var(--fg-muted)",
                      wordBreak: "break-word",
                    }}
                  >
                    {c.tool === "run.verification" && !c.error ? (
                      <Verdicts args={c.args} />
                    ) : (
                      c.error || c.args
                    )}
                  </span>
                  <span style={{ width: 90 }}>
                    <StatePill state={c.outcome} />
                  </span>
                </div>
              ))}
            </>
          )
        }
      </Async>
    </Panel>
  );
}

interface Verdict {
  criterion: string;
  met: boolean;
  evidence: string;
}

/** Verdicts renders the run's verification: each acceptance criterion, whether
 * the evidence showed it met, and what that judgement rests on. A run is only
 * "succeeded" when every one is met, so this is the reason for its state. */
function Verdicts({ args }: { args: string }) {
  let parsed: { verdicts?: Verdict[]; note?: string };
  try {
    parsed = JSON.parse(args) as { verdicts?: Verdict[]; note?: string };
  } catch {
    return <>{args}</>;
  }
  if (!parsed.verdicts || parsed.verdicts.length === 0) {
    return <>{parsed.note ?? "No acceptance criteria were judged."}</>;
  }
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
      {parsed.verdicts.map((v, i) => (
        <div key={i}>
          <span style={{ color: v.met ? "var(--ok)" : "var(--bad)" }}>
            {v.met ? "met" : "not met"}
          </span>{" "}
          <span style={{ color: "var(--fg)" }}>{v.criterion}</span>
          {v.evidence ? (
            <span style={{ color: "var(--fg-faint)" }}> — {v.evidence}</span>
          ) : null}
        </div>
      ))}
    </div>
  );
}
