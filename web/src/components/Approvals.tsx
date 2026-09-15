import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api, enc } from "../lib/api";
import type { Approval, ApprovalList } from "../lib/types";
import { Empty, StatePill } from "./ui";
import { Dialog } from "./Dialog";

type Asking = { approval: Approval; decision: "approved" | "denied" } | null;

/** ApprovalRows lists approval requests and, for someone allowed to decide,
 * offers approve and deny with a comment.
 *
 * Whether the viewer may decide comes from the service (can_decide), and a
 * request the viewer authored is shown without buttons: the service refuses
 * an author's decision, and offering a button that is always refused would
 * be the screen pretending otherwise. */
export function ApprovalRows({
  org,
  list,
  showRun,
  emptyText,
}: {
  org: string;
  list: ApprovalList;
  showRun: boolean;
  emptyText: string;
}) {
  const qc = useQueryClient();
  const [asking, setAsking] = useState<Asking>(null);

  const decide = useMutation({
    mutationFn: (v: { id: string; decision: string; comment: string }) =>
      api.post<Approval>(
        `/api/v1/orgs/${enc(org)}/approvals/${enc(v.id)}/decision`,
        { decision: v.decision, comment: v.comment },
      ),
    onSuccess: () => {
      setAsking(null);
      qc.invalidateQueries({ queryKey: ["approvals"] });
      qc.invalidateQueries({ queryKey: ["proof"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });

  if (list.approvals.length === 0) return <Empty>{emptyText}</Empty>;

  return (
    <>
      {asking ? (
        <Dialog
          title={
            asking.decision === "approved"
              ? `Approve: ${asking.approval.action_name}`
              : `Deny: ${asking.approval.action_name}`
          }
          description={
            <>
              {asking.approval.reason}
              <br />
              This decision holds for commit{" "}
              <code>{asking.approval.head_sha.slice(0, 12)}</code> only. A later
              push to the change asks again.
            </>
          }
          submitLabel={asking.decision === "approved" ? "Approve" : "Deny"}
          fields={[
            {
              name: "comment",
              label: "Comment",
              type: "textarea",
              required: asking.decision === "denied",
              help: "Recorded with the decision as the run's proof.",
            },
          ]}
          busy={decide.isPending}
          error={decide.error}
          onSubmit={(v) =>
            decide.mutate({
              id: asking.approval.id,
              decision: asking.decision,
              comment: v.comment ?? "",
            })
          }
          onClose={() => setAsking(null)}
        />
      ) : null}

      {list.approvals.map((a) => {
        const ownChange = a.author_id !== "" && a.author_id === list.viewer_id;
        const decidable =
          a.decision === "pending" && list.can_decide && !ownChange;
        return (
          <div
            key={a.id}
            style={{
              display: "flex",
              alignItems: "flex-start",
              gap: 12,
              padding: "12px 14px",
              borderBottom: "1px solid var(--line)",
            }}
          >
            <div style={{ flex: 1, minWidth: 0 }}>
              <div
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: 8,
                  flexWrap: "wrap",
                }}
              >
                <span style={{ font: "600 13px var(--sans)" }}>
                  {a.action_name}
                </span>
                <StatePill state={a.decision} />
                {showRun && a.run ? (
                  <Link
                    to={`/runs/${enc(a.run.repo)}/${a.run.number}`}
                    style={{ font: "12px var(--mono)", color: "var(--link)" }}
                  >
                    {a.run.repo}#{a.run.number} · {a.run.title}
                  </Link>
                ) : null}
              </div>
              <div
                style={{
                  font: "12px var(--sans)",
                  color: "var(--fg-dim)",
                  marginTop: 4,
                }}
              >
                {a.reason}
              </div>
              <div
                style={{
                  font: "11px var(--mono)",
                  color: "var(--fg-muted)",
                  marginTop: 4,
                  wordBreak: "break-all",
                }}
              >
                {describe(a)}
              </div>
              {a.decided_by ? (
                <div
                  style={{
                    font: "11px var(--mono)",
                    color: "var(--fg-faint)",
                    marginTop: 4,
                  }}
                >
                  {a.decision} by {a.decided_by.slice(0, 8)}
                  {a.comment ? `: ${a.comment}` : ""}
                </div>
              ) : null}
              {a.decision === "pending" && ownChange ? (
                <div
                  style={{
                    font: "11px var(--sans)",
                    color: "var(--fg-faint)",
                    marginTop: 4,
                  }}
                >
                  You authored this change, so someone else decides.
                </div>
              ) : null}
            </div>
            {decidable ? (
              <div style={{ display: "flex", gap: 6, flex: "none" }}>
                <button
                  onClick={() => {
                    decide.reset();
                    setAsking({ approval: a, decision: "approved" });
                  }}
                  style={buttonStyle("var(--ok)")}
                >
                  Approve
                </button>
                <button
                  onClick={() => {
                    decide.reset();
                    setAsking({ approval: a, decision: "denied" });
                  }}
                  style={buttonStyle("var(--bad)")}
                >
                  Deny
                </button>
              </div>
            ) : null}
          </div>
        );
      })}
    </>
  );
}

/** describe says which files, which commit, and who made the change. */
function describe(a: Approval): string {
  const author = a.author_kind === "agent" ? "an agent" : "a person";
  return `${a.paths.join(", ")} · head ${a.head_sha.slice(0, 12)} · authored by ${author}`;
}

function buttonStyle(color: string) {
  return {
    padding: "4px 10px",
    border: "1px solid var(--line-2)",
    borderRadius: 7,
    background: "transparent",
    color,
    font: "11px var(--sans)",
    cursor: "pointer",
  } as const;
}
