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
  Async,
  Empty,
  Failed,
  Loading,
  Page,
  Panel,
  PanelHead,
  Pill,
  StatePill,
} from "../components/ui";
import { Dialog } from "../components/Dialog";
import type {
  Approval,
  ApprovalList,
  GateConfig,
  GateProposal,
} from "../lib/types";

/** The default branch's gate configuration, as listGateConfig serves it. */
interface GateConfigAnswer {
  ref: string;
  commit_sha: string;
  gates: GateConfig[];
}

/** One rule of the deployment approval policy, as approvalPolicy serves it.
 * action is already the human phrasing; policy is one of the Decision values,
 * with " / forbidden without the capability" appended where a grant decides. */
interface PolicyRule {
  action: string;
  policy: string;
}

/** A gate awaiting a change proposal: which repository's gate, and what the
 * current definition says, so the dialog can pre-fill from it. */
type Proposing = { repo: string; gate: GateConfig } | null;

/** A pending decision on an approval request, and which way it goes —
 * the dialog is shared, and which way decides its title and severity. */
type Deciding = { approval: Approval; decision: "approved" | "denied" } | null;

/** PROPOSAL_TONE tints the approval-policy rules, whose values are the
 * platform's Decision names — the same vocabulary the policy endpoint serves,
 * so a rule's colour agrees with its word. */
const PROPOSAL_TONE: Record<string, [string, string]> = {
  automatic: ["var(--ok-bg)", "var(--ok)"],
  policy: ["var(--info-bg)", "var(--link)"],
  human: ["var(--warn-bg)", "var(--warn)"],
  forbidden: ["var(--bad-bg)", "var(--bad)"],
};

/** Rules & gates is the platform's governance in one place: the approval
 * requests a change raised and waits on a person for, the gate definitions
 * each repository's default branch declares, and the approval policy that
 * decides what else needs a person.
 *
 * Gate definitions cannot be edited here, by design: a proposal turns into
 * an Engineering Run on its own branch and is reviewed like any other change,
 * so weakening a gate is itself gated. Nothing on this screen writes the
 * default branch. */
export function Rules() {
  const w = useWorkspace();
  const repos = scopedRepos(w);
  const qc = useQueryClient();
  const [proposing, setProposing] = useState<Proposing>(null);
  const [deciding, setDeciding] = useState<Deciding>(null);
  /** proposed holds the run the last proposal became, until dismissed — a
   * proposal's outcome is a link to follow, not a silent refresh. */
  const [proposed, setProposed] = useState<{
    repo: string;
    result: GateProposal;
  } | null>(null);

  const gateQueries = useQueries({
    queries: repos.map((r) => ({
      queryKey: ["gate-config", w.org, r.name],
      queryFn: () =>
        api.get<GateConfigAnswer>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(r.name)}/gates`,
        ),
      enabled: w.org !== null,
    })),
  });

  const approvals = useQuery({
    queryKey: ["approvals", w.org],
    queryFn: () =>
      api.get<ApprovalList>(`/api/v1/orgs/${enc(w.org!)}/approvals`),
    enabled: w.org !== null,
    retry: false,
  });

  const policy = useQuery({
    queryKey: ["approval-policy"],
    queryFn: () => api.get<{ rules: PolicyRule[] }>("/api/v1/approvals/policy"),
  });

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["gate-config"] });
    qc.invalidateQueries({ queryKey: ["approvals"] });
    qc.invalidateQueries({ queryKey: ["runs"] });
  };

  const propose = useMutation({
    mutationFn: async ({
      target,
      required,
      paramsText,
    }: {
      target: NonNullable<Proposing>;
      required: string;
      paramsText: string;
    }) => {
      // required is the gate file's `required` flag; "keep" sends nothing and
      // leaves it as the branch has it. A plain tri-state select cannot send
      // "absent", so the mapping lives here rather than in the dialog.
      const body: { enabled?: boolean; params?: unknown } = {};
      if (required === "required") body.enabled = true;
      if (required === "advice") body.enabled = false;
      if (paramsText.trim() !== "") {
        let parsed: unknown;
        try {
          parsed = JSON.parse(paramsText);
        } catch {
          throw new Error("params must be valid JSON");
        }
        if (
          parsed === null ||
          typeof parsed !== "object" ||
          Array.isArray(parsed)
        )
          throw new Error("params must be a JSON object");
        body.params = parsed;
      }
      const result = await api.post<GateProposal>(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(target.repo)}/gates/${enc(
          target.gate.name,
        )}/proposals`,
        body,
      );
      return { repo: target.repo, result };
    },
    onSuccess: (r) => {
      setProposing(null);
      setProposed(r);
      refresh();
    },
  });

  const decideApproval = useMutation({
    mutationFn: (v: { id: string; decision: string; comment: string }) =>
      api.post<Approval>(
        `/api/v1/orgs/${enc(w.org!)}/approvals/${enc(v.id)}/decision`,
        { decision: v.decision, comment: v.comment },
      ),
    onSuccess: () => {
      setDeciding(null);
      refresh();
    },
  });

  const gatesLoading = gateQueries.some((q) => q.isLoading);
  const gatesError = gateQueries.find((q) => q.error)?.error;
  const gatePanels = gateQueries.flatMap((q, i) =>
    q.data ? [{ repo: repos[i]!.name, answer: q.data }] : [],
  );

  const list = approvals.data;
  const pending = (list?.approvals ?? []).filter(
    (a) => a.decision === "pending",
  );
  const decided = (list?.approvals ?? []).filter(
    (a) => a.decision !== "pending",
  );

  return (
    <Page
      title="Rules & gates"
      subtitle="What a change must pass, what is waiting on a person, and what the platform requires before each action"
    >
      {proposed ? (
        <Panel style={{ marginBottom: 14 }}>
          <PanelHead>
            PROPOSAL FILED · {proposed.repo}
            <button
              type="button"
              onClick={() => setProposed(null)}
              style={dismissLink}
            >
              dismiss
            </button>
          </PanelHead>
          <div style={{ padding: "10px 14px", font: "13px var(--sans)" }}>
            The change was never written to the default branch. It is run{" "}
            <Link
              to={`/runs/${enc(proposed.repo)}/${proposed.result.run_number}`}
            >
              #{proposed.result.run_number}
            </Link>{" "}
            on branch{" "}
            <span style={{ font: "12px var(--mono)", color: "var(--link)" }}>
              {proposed.result.branch}
            </span>
            , and lands only if the review merges it.
          </div>
        </Panel>
      ) : null}

      {proposing ? (
        <Dialog
          title={`Propose a change to ${proposing.gate.name}`}
          description="The change becomes an Engineering Run on its own branch, reviewed like any other. Nothing here edits the default branch: a gate that judges every change must not be weakenable outside review."
          submitLabel="Open proposal run"
          fields={[
            {
              name: "required",
              label: "Does this gate block merges?",
              type: "select",
              options: ["Keep as declared", "Required", "Advice only"],
              initialValue: "Keep as declared",
              help: "Required blocks a merge until the gate passes. Advice is evaluated and reported but never blocks.",
            },
            {
              name: "params",
              label: "Params (JSON object)",
              type: "textarea",
              initialValue: JSON.stringify(proposing.gate.params, null, 2),
              help: "Leave as-is to keep the current params. Replaced whole, not merged.",
            },
          ]}
          busy={propose.isPending}
          error={propose.error}
          onSubmit={(v) =>
            propose.mutate({
              target: proposing,
              required:
                v.required === "Required"
                  ? "required"
                  : v.required === "Advice only"
                    ? "advice"
                    : "keep",
              paramsText: v.params ?? "",
            })
          }
          onClose={() => setProposing(null)}
        />
      ) : null}

      {deciding ? (
        <Dialog
          title={`${deciding.decision === "approved" ? "Approve" : "Deny"}: ${deciding.approval.action_name}`}
          description={
            deciding.decision === "approved"
              ? "The action proceeds. The decision is recorded under your name; the author can never approve their own change."
              : "The action is refused with your reason on record."
          }
          submitLabel={deciding.decision === "approved" ? "Approve" : "Deny"}
          fields={[
            {
              name: "comment",
              label: "Comment",
              type: "textarea",
              required: deciding.decision === "denied",
              help: "Recorded with the decision, in the run's approval history.",
            },
          ]}
          busy={decideApproval.isPending}
          error={decideApproval.error}
          onSubmit={(v) =>
            decideApproval.mutate({
              id: deciding.approval.id,
              decision: deciding.decision,
              comment: v.comment ?? "",
            })
          }
          onClose={() => setDeciding(null)}
        />
      ) : null}

      <div style={{ display: "grid", gap: 14 }}>
        <Panel>
          <PanelHead>
            APPROVAL REQUESTS
            <span style={{ color: "var(--fg-faint)" }}>
              {list ? list.approvals.length : ""}
            </span>
            {pending.length > 0 ? (
              <span style={{ color: "var(--warn)" }}>
                {pending.length} awaiting a decision
              </span>
            ) : null}
            <div style={{ flex: 1 }} />
            {list && !list.can_decide ? (
              <span
                style={{ font: "10px var(--sans)", color: "var(--fg-faint)" }}
              >
                an owner or admin decides, never the change's author
              </span>
            ) : null}
          </PanelHead>
          {w.org === null ? (
            <Empty>No organization selected.</Empty>
          ) : (
            <Async query={approvals}>
              {(d) =>
                d.approvals.length === 0 ? (
                  <Empty>
                    No approval request is open in this organization.
                  </Empty>
                ) : (
                  <>
                    {[...pending, ...decided].map((a) => (
                      <ApprovalRow
                        key={a.id}
                        approval={a}
                        canDecide={d.can_decide}
                        viewerId={d.viewer_id}
                        busy={decideApproval.isPending}
                        onDecide={(decision) => {
                          decideApproval.reset();
                          setDeciding({ approval: a, decision });
                        }}
                      />
                    ))}
                  </>
                )
              }
            </Async>
          )}
        </Panel>

        <Panel>
          <PanelHead>
            GATE DEFINITIONS
            <span style={{ color: "var(--fg-faint)" }}>
              {repos.length > 0
                ? `${repos.length} repositor${repos.length === 1 ? "y" : "ies"}`
                : ""}
            </span>
          </PanelHead>
          {repos.length === 0 ? (
            <Empty>
              Select a repository in the workspace switcher — gates are declared
              per repository, in .novaforge/gates on its default branch.
            </Empty>
          ) : gatesLoading ? (
            <Loading />
          ) : gatesError ? (
            <div style={{ padding: 14 }}>
              <Failed error={gatesError} />
            </div>
          ) : (
            gatePanels.map(({ repo, answer }) => (
              <GateTable
                key={repo}
                repo={repo}
                answer={answer}
                onPropose={setProposing}
              />
            ))
          )}
        </Panel>

        <Async query={policy} empty="No approval policy is available.">
          {(d) => (
            <Panel>
              <PanelHead>APPROVAL POLICY</PanelHead>
              <div
                style={{
                  padding: "10px 14px",
                  font: "12px var(--sans)",
                  color: "var(--fg-dim)",
                }}
              >
                Fixed in the platform, not configurable here: the model never
                decides its own permissions, and no request can widen a rule.
              </div>
              {d.rules.map((r) => {
                // The policy field appends the grant caveat after the Decision
                // value; the tone comes from the Decision alone.
                const family = r.policy.split(" ")[0] ?? "";
                const [bg, fg] = PROPOSAL_TONE[family] ?? [
                  "rgba(139,145,160,.13)",
                  "var(--fg-muted)",
                ];
                return (
                  <div
                    key={r.action}
                    style={{
                      display: "flex",
                      alignItems: "center",
                      gap: 12,
                      padding: "9px 14px",
                      borderTop: "1px solid var(--line)",
                    }}
                  >
                    <span style={{ flex: 1, font: "13px var(--sans)" }}>
                      {r.action}
                    </span>
                    <Pill bg={bg} fg={fg}>
                      {r.policy}
                    </Pill>
                  </div>
                );
              })}
            </Panel>
          )}
        </Async>
      </div>
    </Page>
  );
}

/** ApprovalRow is one request a change raised. It names the change — run,
 * repository, who asked — because "approve the schema change" is meaningless
 * without knowing which one. */
function ApprovalRow({
  approval: a,
  canDecide,
  viewerId,
  busy,
  onDecide,
}: {
  approval: Approval;
  canDecide: boolean;
  viewerId: string;
  busy: boolean;
  onDecide: (decision: "approved" | "denied") => void;
}) {
  const mine = a.author_id === viewerId;
  const decided = a.decision !== "pending";
  return (
    <div
      style={{
        padding: "11px 14px",
        borderBottom: "1px solid var(--line)",
        opacity: decided ? 0.55 : 1,
      }}
    >
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 10,
          flexWrap: "wrap",
        }}
      >
        <span style={{ font: "600 13px var(--sans)" }}>{a.action_name}</span>
        <StatePill state={a.decision} />
        {a.run ? (
          <span style={{ font: "12px var(--mono)", color: "var(--fg-muted)" }}>
            <Link to={`/runs/${enc(a.run.repo)}/${a.run.number}`}>
              #{a.run.number} {a.run.title}
            </Link>{" "}
            · {a.run.repo}
            {a.run.author_kind === "agent" && a.run.agent_name
              ? ` · asked by agent ${a.run.agent_name}`
              : ""}
          </span>
        ) : (
          <span style={{ font: "12px var(--mono)", color: "var(--fg-faint)" }}>
            run {a.run_id.slice(0, 8)}
          </span>
        )}
        <div style={{ flex: 1 }} />
        <span style={{ font: "10px var(--mono)", color: "var(--fg-faint)" }}>
          {a.created_at.slice(0, 10)}
        </span>
      </div>
      {a.deployment && Object.keys(a.deployment).length > 0 ? (
        <div
          style={{
            font: "12px var(--mono)",
            color: "var(--fg-muted)",
            marginTop: 4,
          }}
        >
          {a.deployment.environment} →{" "}
          {a.deployment.destination || a.deployment.target}
          {a.deployment.artifact ? ` · ${a.deployment.artifact}` : ""}
        </div>
      ) : null}
      {a.paths.length > 0 ? (
        <div
          style={{
            font: "11px var(--mono)",
            color: "var(--fg-faint)",
            marginTop: 4,
            wordBreak: "break-all",
          }}
        >
          {a.paths.slice(0, 6).join(", ")}
          {a.paths.length > 6 ? ` · ${a.paths.length - 6} more` : ""}
        </div>
      ) : null}
      {a.comment ? (
        <div
          style={{
            font: "12px var(--sans)",
            color: "var(--fg-dim)",
            marginTop: 4,
          }}
        >
          {a.comment}
        </div>
      ) : null}
      {!decided ? (
        <div style={{ marginTop: 8, display: "flex", gap: 8 }}>
          {canDecide && !mine ? (
            <>
              <button
                disabled={busy}
                onClick={() => onDecide("approved")}
                style={decideButton("var(--accent)", "#fff")}
              >
                Approve
              </button>
              <button
                disabled={busy}
                onClick={() => onDecide("denied")}
                style={decideButton("transparent", "var(--bad)", "var(--bad)")}
              >
                Deny
              </button>
            </>
          ) : (
            <span
              style={{ font: "11px var(--sans)", color: "var(--fg-faint)" }}
            >
              {mine
                ? "You asked for this — the author cannot decide it."
                : "Only an owner or admin may decide this."}
            </span>
          )}
        </div>
      ) : null}
    </div>
  );
}

/** GateTable is one repository's gates, headed by the ref they were read
 * from: a definition is only meaningful as of a commit, and the ref names
 * which one this screen saw. */
function GateTable({
  repo,
  answer,
  onPropose,
}: {
  repo: string;
  answer: GateConfigAnswer;
  onPropose: (p: Proposing) => void;
}) {
  return (
    <div>
      <div
        style={{
          padding: "10px 14px 4px",
          font: "600 12px var(--mono)",
          color: "var(--link)",
          display: "flex",
          gap: 10,
          alignItems: "baseline",
        }}
      >
        {repo}
        <span style={{ font: "10px var(--mono)", color: "var(--fg-faint)" }}>
          {answer.ref} @ {answer.commit_sha.slice(0, 10)}
        </span>
      </div>
      {answer.gates.length === 0 ? (
        <Empty>This repository declares no gate under .novaforge/gates.</Empty>
      ) : (
        answer.gates.map((g) => (
          <div
            key={g.name}
            style={{
              display: "flex",
              alignItems: "center",
              gap: 12,
              padding: "9px 14px",
              borderTop: "1px solid var(--line)",
            }}
          >
            <span
              style={{
                font: "13px var(--mono)",
                color: "var(--fg)",
                width: 180,
                flex: "none",
              }}
            >
              {g.name}
            </span>
            <Pill
              bg={g.enabled ? "var(--ok-bg)" : "var(--warn-bg)"}
              fg={g.enabled ? "var(--ok)" : "var(--warn)"}
            >
              {g.enabled ? "required" : "advice"}
            </Pill>
            {!g.declared ? (
              <Pill bg="rgba(139,145,160,.13)" fg="var(--fg-muted)">
                not declared
              </Pill>
            ) : null}
            <span
              style={{
                flex: 1,
                minWidth: 0,
                font: "11px var(--mono)",
                color: "var(--fg-faint)",
                overflow: "hidden",
                textOverflow: "ellipsis",
                whiteSpace: "nowrap",
              }}
              title={g.path}
            >
              {g.path}
              {Object.keys(g.params).length > 0
                ? ` · ${JSON.stringify(g.params)}`
                : ""}
            </span>
            <button
              type="button"
              onClick={() => onPropose({ repo, gate: g })}
              style={decideButton(
                "transparent",
                "var(--link)",
                "var(--accent)",
              )}
            >
              Propose change
            </button>
          </div>
        ))
      )}
    </div>
  );
}

function decideButton(
  bg: string,
  fg: string,
  border?: string,
): React.CSSProperties {
  return {
    padding: "4px 11px",
    background: bg,
    color: fg,
    border: `1px solid ${border ?? "transparent"}`,
    borderRadius: 7,
    font: "600 11px var(--sans)",
    cursor: "pointer",
    flex: "none",
  };
}

const dismissLink: React.CSSProperties = {
  background: "none",
  border: "none",
  color: "var(--fg-faint)",
  font: "10px var(--mono)",
  cursor: "pointer",
  padding: 0,
  marginLeft: "auto",
};
