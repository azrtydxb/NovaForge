import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { Async, Empty, Failed, StatePill } from "./ui";
import { Dialog, NewButton } from "./Dialog";

type Attempt = {
  id: string;
  agent_id: string;
  role: string;
  model: string;
  state: string;
  verdict: string;
  summary: string;
  error: string;
  tokens_used: string;
  cost_used_micros: string;
  tokens_available: boolean;
  cost_available: boolean;
};
type Request = {
  id: string;
  source_sha: string;
  target_sha: string;
  state: string;
  detail: string;
  attempts: Attempt[];
};
export function AgentReviewRequests({
  org,
  runID,
  sourceSHA,
  open,
}: {
  org: string;
  runID: string;
  sourceSHA: string | undefined;
  open: boolean;
}) {
  const base = `/api/v1/orgs/${enc(org)}/engineering-runs/${enc(runID)}/agent-reviews`;
  const qc = useQueryClient();
  const [confirm, setConfirm] = useState<string | null>(null);
  const history = useQuery({
    queryKey: ["agent-review-requests", base],
    queryFn: () => api.get<{ available: boolean; requests: Request[] }>(base),
    refetchInterval: (q) =>
      q.state.data?.requests.some(
        (r) => r.state === "queued" || r.state === "running",
      )
        ? 2000
        : false,
  });
  const request = useMutation({
    mutationFn: () => api.post(base, { expected_source_sha: confirm }),
    onSuccess: () => {
      setConfirm(null);
      void qc.invalidateQueries();
    },
  });
  return (
    <div style={{ padding: 14, borderTop: "1px solid var(--line)" }}>
      <Async query={history}>
        {(data) => (
          <>
            {data.available && open && sourceSHA ? (
              <NewButton
                label="Request agent review"
                onClick={() => {
                  request.reset();
                  setConfirm(sourceSHA);
                }}
              />
            ) : null}
            {!data.available ? (
              <Empty>
                Independent agent review is not configured in this deployment.
              </Empty>
            ) : null}
            {data.requests.map((r) => (
              <div key={r.id} style={{ marginTop: 10 }}>
                <StatePill state={r.state} /> · source{" "}
                <code>{r.source_sha}</code> · target <code>{r.target_sha}</code>
                <p>{r.detail || "Waiting for an independent reviewer."}</p>
                {r.attempts.map((a) => (
                  <div key={a.id} style={{ margin: 8 }}>
                    {a.role} · {a.agent_id} · {a.model} ·{" "}
                    <StatePill state={a.state} />
                    <div>
                      {a.verdict} {a.summary} {a.error}
                    </div>
                    <div>
                      Tokens:{" "}
                      {a.tokens_available ? a.tokens_used : "unavailable"} ·
                      Cost:{" "}
                      {a.cost_available
                        ? `${a.cost_used_micros} micros`
                        : "unavailable"}
                    </div>
                  </div>
                ))}
              </div>
            ))}
          </>
        )}
      </Async>
      {request.error && !confirm ? <Failed error={request.error} /> : null}
      {confirm ? (
        <Dialog
          title="Request independent agent review"
          submitLabel="Queue review"
          description={
            <p>
              Review source <code>{confirm}</code> with the configured
              independent roles. This invokes the model and may incur usage.
              Retries of the same source and target return the existing request,
              never repeat its model calls.
            </p>
          }
          fields={[]}
          busy={request.isPending}
          error={request.error}
          onSubmit={() => request.mutate()}
          onClose={() => setConfirm(null)}
        />
      ) : null}
    </div>
  );
}
