import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { Confirm } from "./Dialog";

/** ACTIVE_RUN_STATES are the Agent Run states that can still be cancelled.
 * Every other state is terminal: agent-runtime refuses to move a run out of
 * one, so offering Cancel there would only lead to a refusal. */
export const ACTIVE_RUN_STATES = new Set(["queued", "running"]);

/** CancelAgentRun stops an Agent Run, after asking. Cancelling ends the run
 * at its next step — anything it already committed to its branch stays — and
 * cannot be resumed, which is why it is never a single click. */
export function CancelAgentRun({
  org,
  runId,
  label = "Cancel",
}: {
  org: string;
  runId: string;
  label?: string;
}) {
  const [asking, setAsking] = useState(false);
  const qc = useQueryClient();

  const cancel = useMutation({
    mutationFn: () =>
      api.del(`/api/v1/orgs/${enc(org)}/agent-runs/${enc(runId)}`),
    onSuccess: () => {
      setAsking(false);
      // A cancelled run changes what several screens show: the runs on its
      // Work Item, the CI job it may be executing, the agent's run history
      // and the dashboard's running count.
      for (const key of [
        "agent-runs",
        "ci-run",
        "ci-runs",
        "agent-stats",
        "dashboard",
      ]) {
        qc.invalidateQueries({ queryKey: [key] });
      }
    },
  });

  return (
    <>
      <button
        onClick={() => {
          cancel.reset();
          setAsking(true);
        }}
        style={{
          background: "transparent",
          border: "1px solid #e5534b66",
          borderRadius: 6,
          color: "var(--bad)",
          font: "11px var(--sans)",
          padding: "3px 9px",
          cursor: "pointer",
          flex: "none",
        }}
      >
        {label}
      </button>
      {asking ? (
        <Confirm
          title="Cancel this Agent Run?"
          body={
            <>
              The run stops at its next step and cannot be resumed. Anything it
              has already committed to its branch stays there.
              <div
                style={{
                  font: "11px var(--mono)",
                  color: "var(--fg-faint)",
                  marginTop: 8,
                }}
              >
                {runId}
              </div>
            </>
          }
          confirmLabel="Cancel run"
          danger
          busy={cancel.isPending}
          error={cancel.error}
          onConfirm={() => cancel.mutate()}
          onClose={() => setAsking(false)}
        />
      ) : null}
    </>
  );
}
