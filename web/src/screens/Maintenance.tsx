import { useState } from "react";
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Link } from "react-router-dom";
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
import { Dialog } from "../components/Dialog";
import type { Agent, MaintenanceProposal, OrgMember } from "../lib/types";

const TYPE_TONE: Record<string, string> = {
  security: "var(--bad)",
  upgrade: "var(--warn)",
  tech_debt: "var(--fg-muted)",
  bug: "var(--warn)",
  documentation: "var(--fg-muted)",
  refactor: "var(--violet)",
};

/** MYSELF is the approval choice that keeps the Work Item with the approver. */
const MYSELF = "Me";

type Deciding = {
  kind: "approve" | "dismiss";
  proposal: MaintenanceProposal;
  repo: string;
};

/** Maintenance lists what the scanners found and proposed. A proposal is a
 * Work Item that waits for a person: nothing executes it until someone
 * approves it — agent-runtime refuses to start a run against one that is
 * still waiting — and a dismissed one is closed with the reason recorded on
 * the Work Item. The platform proposes, a person disposes. */
export function Maintenance() {
  const w = useWorkspace();
  const repos = scopedRepos(w);
  const qc = useQueryClient();
  const [deciding, setDeciding] = useState<Deciding | null>(null);

  const queries = useQueries({
    queries: repos.map((r) => ({
      queryKey: ["maintenance", w.org, r.name],
      queryFn: () =>
        api.get<{ proposals: MaintenanceProposal[] }>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(r.name)}/maintenance`,
        ),
      enabled: w.org !== null,
    })),
  });

  const agents = useQuery({
    queryKey: ["agents", w.org],
    queryFn: () =>
      api.get<{ agents: Agent[] }>(`/api/v1/orgs/${enc(w.org!)}/agents`),
    enabled: w.org !== null,
  });
  const enabledAgents = (agents.data?.agents ?? []).filter((a) => a.enabled);

  const members = useQuery({
    queryKey: ["members", w.org],
    queryFn: () =>
      api.get<{ members: OrgMember[] }>(`/api/v1/orgs/${enc(w.org!)}/members`),
    enabled: w.org !== null,
  });
  // Who decided, and who holds the item, are ids; the names come from the
  // organization's own member and agent lists, and an id that is in neither
  // is shown as the id rather than guessed at.
  const nameOf = (id: string, kind: string) =>
    (kind === "agent"
      ? agents.data?.agents.find((a) => a.id === id)?.name
      : members.data?.members.find((m) => m.user_id === id)?.username) ??
    id.slice(0, 8);

  const invalidate = () => {
    for (const key of [
      "maintenance",
      "work",
      "work-item",
      "agent-runs",
      "dashboard",
    ]) {
      qc.invalidateQueries({ queryKey: [key] });
    }
  };

  const approve = useMutation({
    mutationFn: async ({ d, choice }: { d: Deciding; choice: string }) => {
      const base = `/api/v1/orgs/${enc(w.org!)}/repos/${enc(d.repo)}`;
      const option = approvalOptions(enabledAgents).find(
        (o) => o.label === choice,
      );
      if (!option) throw new Error("choose who takes the work");
      await api.post(
        `${base}/maintenance/${enc(d.proposal.fingerprint)}/approve`,
        option.agent ? { agent_id: option.agent.id } : {},
      );
      // Starting the run is a second, ordinary request: the approval is
      // already recorded, so a run that fails to start leaves an approved,
      // assigned Work Item someone can start from its own page.
      if (option.agent && option.start) {
        await api.post(`${base}/agent-runs`, {
          agent_id: option.agent.id,
          work_item_key: d.proposal.work_item_key,
        });
      }
    },
    onSuccess: () => {
      setDeciding(null);
      invalidate();
    },
    onError: invalidate,
  });

  const dismiss = useMutation({
    mutationFn: ({ d, reason }: { d: Deciding; reason: string }) =>
      api.post(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(d.repo)}/maintenance/${enc(
          d.proposal.fingerprint,
        )}/dismiss`,
        { reason },
      ),
    onSuccess: () => {
      setDeciding(null);
      invalidate();
    },
  });

  const loading = queries.some((q) => q.isLoading);
  const firstError = queries.find((q) => q.error)?.error;
  const rows = queries.flatMap((q, i) =>
    (q.data?.proposals ?? []).map((p) => ({ p, repo: repos[i]!.name })),
  );
  const awaiting = rows.filter(({ p }) => awaitingApproval(p)).length;

  return (
    <Page
      title="Maintenance"
      subtitle="Outdated dependencies, CVEs, flaky tests, dead code, coverage, docs drift, performance and architecture — each proposed as work, executed only once approved"
    >
      {deciding?.kind === "approve" ? (
        <Dialog
          title={`Approve ${deciding.proposal.work_item_key}`}
          submitLabel="Approve"
          fields={[
            {
              name: "assignee",
              label: "Who takes the work",
              type: "select",
              options: approvalOptions(enabledAgents).map((o) => o.label),
              required: true,
              help: "Approving records you as the approver. An agent can be started on it only after this.",
            },
          ]}
          busy={approve.isPending}
          error={approve.error}
          onSubmit={(v) =>
            approve.mutate({ d: deciding, choice: v.assignee ?? MYSELF })
          }
          onClose={() => setDeciding(null)}
        />
      ) : null}
      {deciding?.kind === "dismiss" ? (
        <Dialog
          title={`Dismiss ${deciding.proposal.work_item_key}`}
          submitLabel="Dismiss"
          fields={[
            {
              name: "reason",
              label: "Why this should not be done",
              type: "textarea",
              required: true,
              help: "Recorded on the Work Item, which is closed. A later scan does not reopen it.",
            },
          ]}
          busy={dismiss.isPending}
          error={dismiss.error}
          onSubmit={(v) =>
            dismiss.mutate({ d: deciding, reason: v.reason ?? "" })
          }
          onClose={() => setDeciding(null)}
        />
      ) : null}

      <Panel>
        <PanelHead>
          PROPOSALS
          <span style={{ color: "var(--fg-faint)" }}>{rows.length}</span>
          {awaiting > 0 ? (
            <span style={{ color: "var(--warn)" }}>
              {awaiting} awaiting approval
            </span>
          ) : null}
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
                opacity: p.resolved || p.decision === "dismissed" ? 0.55 : 1,
              }}
            >
              <Pill
                bg={`${TYPE_TONE[p.work_item_type] ?? "var(--fg-muted)"}22`}
                fg={TYPE_TONE[p.work_item_type] ?? "var(--fg-muted)"}
              >
                {p.work_item_type}
              </Pill>
              <span style={{ flex: 1, minWidth: 0 }}>
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
                  <Link to={`/work/${enc(repo)}/${enc(p.work_item_key)}`}>
                    {p.work_item_key}
                  </Link>{" "}
                  · {repo}
                  {p.resolved ? " · no longer reproducing" : ""}
                  {p.decision === "approved"
                    ? ` · approved by ${nameOf(p.decided_by, "user")}${
                        p.assignee_id
                          ? `, assigned to ${nameOf(p.assignee_id, p.assignee_kind)}`
                          : ""
                      }`
                    : ""}
                  {p.decision === "dismissed"
                    ? ` · dismissed by ${nameOf(p.decided_by, "user")}: ${p.dismiss_reason}`
                    : ""}
                </div>
              </span>
              {awaitingApproval(p) ? (
                <>
                  <Pill bg="var(--warn-bg)" fg="var(--warn)">
                    awaiting approval
                  </Pill>
                  <button
                    onClick={() => {
                      approve.reset();
                      setDeciding({ kind: "approve", proposal: p, repo });
                    }}
                    style={approveButton}
                  >
                    Approve
                  </button>
                  <button
                    onClick={() => {
                      dismiss.reset();
                      setDeciding({ kind: "dismiss", proposal: p, repo });
                    }}
                    style={dismissButton}
                  >
                    Dismiss
                  </button>
                </>
              ) : (
                <span
                  style={{
                    font: "11px var(--mono)",
                    color: "var(--fg-muted)",
                  }}
                >
                  {p.decision || p.state}
                </span>
              )}
            </div>
          ))
        )}
      </Panel>
    </Page>
  );
}

/** awaitingApproval mirrors the service's own rule: undecided, and still
 * reproducing. */
function awaitingApproval(p: MaintenanceProposal): boolean {
  return p.decision === "" && !p.resolved;
}

interface ApprovalOption {
  label: string;
  agent?: Agent;
  start?: boolean;
}

/** approvalOptions is who can take an approved proposal: the approver, or an
 * enabled agent of this organization — assigned, or assigned and started. */
function approvalOptions(agents: Agent[]): ApprovalOption[] {
  return [
    { label: MYSELF },
    ...agents.flatMap((a) => [
      { label: `${a.name} (${a.role}) — assign`, agent: a },
      {
        label: `${a.name} (${a.role}) — assign and start a run`,
        agent: a,
        start: true,
      },
    ]),
  ];
}

const approveButton: React.CSSProperties = {
  padding: "4px 11px",
  background: "var(--accent)",
  border: "none",
  borderRadius: 7,
  color: "#fff",
  font: "600 11px var(--sans)",
  cursor: "pointer",
  flex: "none",
};

const dismissButton: React.CSSProperties = {
  padding: "4px 11px",
  background: "transparent",
  border: "1px solid var(--line-2)",
  borderRadius: 7,
  color: "var(--fg-muted)",
  font: "11px var(--sans)",
  cursor: "pointer",
  flex: "none",
};
