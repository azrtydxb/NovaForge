import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import type { EngineeringRun } from "../lib/types";
import { Async, Empty, Panel, PanelHead, StatePill } from "./ui";
import { Dialog, NewButton } from "./Dialog";
import { AgentReviewRequests } from "./AgentReviewRequests";

interface ReviewList {
  current_source_sha: string;
  current_source_available: boolean;
  reviews: {
    reviewer_id: string;
    reviewer_kind: string;
    verdict: string;
    summary: string;
    source_sha: string;
    created_at: string;
  }[];
}

/** A review is about inspected bytes, never whatever a branch points at later. */
export function RunReviews({
  base,
  org,
  repo,
  run,
}: {
  base: string;
  org: string;
  repo: string;
  run: EngineeringRun;
}) {
  const qc = useQueryClient();
  const [reviewing, setReviewing] = useState<string | null>(null);
  const reviews = useQuery({
    queryKey: ["reviews", base],
    queryFn: () =>
      api.get<ReviewList>(
        `/api/v1/orgs/${enc(org)}/engineering-runs/${enc(run.id)}/reviews`,
      ),
  });
  const diff = useQuery({
    queryKey: ["review-diff", base, reviewing, run.target_ref],
    queryFn: () =>
      api.get<{ unified: string }>(
        `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}/diff?from=${enc(run.target_ref)}&to=${enc(reviewing!)}&merge_base=true`,
      ),
    enabled: reviewing !== null,
    staleTime: Infinity,
  });
  const review = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post(`${base}/reviews`, {
        verdict: v.verdict,
        summary: v.summary,
        expected_source_sha: reviewing,
      }),
    onSuccess: () => {
      setReviewing(null);
      void qc.invalidateQueries();
    },
  });
  return (
    <Panel style={{ marginBottom: 14 }}>
      <PanelHead>
        INDEPENDENT REVIEWS
        <div style={{ flex: 1 }} />
        {run.state === "open" &&
        reviews.data?.current_source_available &&
        reviews.data.current_source_sha ? (
          <NewButton
            label="Review"
            onClick={() => {
              review.reset();
              setReviewing(reviews.data!.current_source_sha);
            }}
          />
        ) : null}
      </PanelHead>
      <Async query={reviews}>
        {(d) => (
          <>
            {!d.current_source_available ? (
              <Empty>
                Current source unavailable; review currency unknown. Submission
                is disabled.
              </Empty>
            ) : null}
            {d.reviews.length === 0 ? (
              <Empty>No independent review recorded.</Empty>
            ) : (
              d.reviews.map((r) => (
                <div
                  key={r.reviewer_id}
                  style={{ padding: 14, borderBottom: "1px solid var(--line)" }}
                >
                  <StatePill state={r.verdict} /> {r.reviewer_kind} ·{" "}
                  {r.reviewer_id}
                  <div style={{ font: "11px var(--mono)", marginTop: 6 }}>
                    {r.source_sha
                      ? `${r.source_sha} · ${!d.current_source_available ? "currency unknown" : r.source_sha === d.current_source_sha ? "current source" : "stale source — cannot authorize merge"}`
                      : "Legacy unbound review — fresh review required"}{" "}
                    · {r.created_at}
                  </div>
                  <div style={{ whiteSpace: "pre-wrap", marginTop: 6 }}>
                    {r.summary}
                  </div>
                </div>
              ))
            )}
          </>
        )}
      </Async>
      <AgentReviewRequests
        org={org}
        runID={run.id}
        sourceSHA={
          reviews.data?.current_source_available
            ? reviews.data.current_source_sha
            : undefined
        }
        open={run.state === "open"}
      />
      {reviewing ? (
        <Dialog
          title={`Review #${run.number}`}
          submitLabel="Submit"
          description={
            <>
              <p>
                Inspect source commit <code>{reviewing}</code>. A later push
                requires a fresh review.
              </p>
              <Async query={diff}>
                {(d) => (
                  <pre
                    style={{
                      maxHeight: 240,
                      overflow: "auto",
                      font: "11px var(--mono)",
                      whiteSpace: "pre-wrap",
                    }}
                  >
                    {d.unified || "No difference at this revision."}
                  </pre>
                )}
              </Async>
            </>
          }
          fields={[
            {
              name: "verdict",
              label: "Verdict",
              type: "select",
              options: ["comment", "approve", "request_changes"],
              required: true,
            },
            { name: "summary", label: "Summary", type: "textarea" },
          ]}
          busy={review.isPending}
          submitDisabled={
            !diff.data ||
            !!diff.error ||
            !reviews.data?.current_source_available
          }
          error={review.error}
          onSubmit={(v) => review.mutate(v)}
          onClose={() => setReviewing(null)}
        />
      ) : null}
    </Panel>
  );
}
