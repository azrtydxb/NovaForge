import { useState, type CSSProperties } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, enc } from "../lib/api";
import { Panel, PanelHead, Failed, Empty, StatePill } from "../components/ui";

const newRequestID = () => {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6]! & 15) | 64;
  bytes[8] = (bytes[8]! & 63) | 128;
  const h = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
};

type Target = { name: string; environment: string; revision: string };
type Deployment = {
  id: string;
  run_id: string;
  target: string;
  artifact: string;
  state: string;
  attempts: { number: number; state: string; error: string; summary: string }[];
  credentials: { attempt: number; resolved_at: string }[];
};

export function Deployments({
  org,
  repo,
  runID,
}: {
  org: string;
  repo: string;
  runID: string;
}) {
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}`;
  const qc = useQueryClient();
  const [target, setTarget] = useState("");
  const [artifact, setArtifact] = useState("");
  const [requestID, setRequestID] = useState(() => newRequestID());

  const targets = useQuery({
    queryKey: ["deployment-targets", org, repo],
    queryFn: () => api.get<{ targets: Target[] }>(`${base}/deployment-targets`),
    retry: false,
  });

  const operations = useQuery({
    queryKey: ["deployments", org, repo],
    queryFn: () => api.get<{ operations: Deployment[] }>(`${base}/deployments`),
    enabled: !!targets.data,
    refetchInterval: 5000,
  });

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["deployments", org, repo] });
    void qc.invalidateQueries({ queryKey: ["approvals", org] });
  };

  const request = useMutation({
    mutationFn: () =>
      api.post<Deployment>(`${base}/deployments`, {
        id: requestID,
        run_id: runID,
        target,
        artifact,
      }),
    onSuccess: () => {
      setRequestID(newRequestID());
      refresh();
    },
  });

  const action = useMutation({
    mutationFn: ({ id, verb }: { id: string; verb: string }) =>
      api.post<Deployment>(`${base}/deployments/${enc(id)}/${verb}`, {}),
    onSettled: refresh,
  });

  const unavailable =
    targets.error instanceof ApiError &&
    [501, 503].includes(targets.error.status);

  return (
    <Panel style={{ marginBottom: 14 }}>
      <PanelHead title="DEPLOYMENTS" />
      <div style={{ padding: 14, display: "grid", gap: 12 }}>
        {unavailable ? (
          <Empty>
            No deployment targets are configured in this installation.
          </Empty>
        ) : null}
        {targets.error && !unavailable ? (
          <Failed error={targets.error} />
        ) : null}
        {targets.data?.targets.length === 0 ? (
          <Empty>
            No deployment targets are configured for this repository.
          </Empty>
        ) : null}

        {!!targets.data?.targets.length && (
          <>
            <p
              style={{
                margin: 0,
                font: "12px/1.6 var(--sans)",
                color: "var(--fg-muted)",
              }}
            >
              Request deployment of an immutable image. An independent reviewer
              must approve the exact target and artifact before execution.
            </p>
            <label style={{ display: "block" }}>
              <span
                style={{
                  display: "block",
                  font: "600 10px var(--sans)",
                  letterSpacing: ".08em",
                  color: "var(--fg-muted)",
                  marginBottom: 5,
                }}
              >
                TARGET
              </span>
              <select
                value={target}
                onChange={(e) => {
                  setTarget(e.target.value);
                  setRequestID(newRequestID());
                }}
                style={field}
              >
                <option value="">Choose a target</option>
                {targets.data.targets.map((t) => (
                  <option key={t.name} value={t.name}>
                    {t.name} · {t.environment}
                  </option>
                ))}
              </select>
            </label>
            <label style={{ display: "block" }}>
              <span
                style={{
                  display: "block",
                  font: "600 10px var(--sans)",
                  letterSpacing: ".08em",
                  color: "var(--fg-muted)",
                  marginBottom: 5,
                }}
              >
                IMAGE DIGEST
              </span>
              <input
                value={artifact}
                placeholder="sha256:…"
                onChange={(e) => {
                  setArtifact(e.target.value);
                  setRequestID(newRequestID());
                }}
                style={field}
              />
            </label>
            <div>
              <button
                disabled={
                  !target ||
                  !/^sha256:[a-f0-9]{64}$/.test(artifact) ||
                  request.isPending
                }
                onClick={() => request.mutate()}
                style={primaryBtn(request.isPending)}
              >
                Request deployment approval
              </button>
            </div>
          </>
        )}

        {request.error ? <Failed error={request.error} /> : null}
        {action.error ? <Failed error={action.error} /> : null}
        {operations.error ? <Failed error={operations.error} /> : null}

        {operations.data?.operations
          .filter((op) => op.run_id === runID)
          .map((op) => (
            <div
              key={op.id}
              style={{
                borderTop: "1px solid var(--line)",
                paddingTop: 12,
                display: "grid",
                gap: 8,
              }}
            >
              <div
                style={{
                  display: "flex",
                  alignItems: "baseline",
                  gap: 10,
                  flexWrap: "wrap",
                }}
              >
                <span style={{ font: "600 13px var(--sans)" }}>
                  {op.target}
                </span>
                <StatePill state={op.state} />
                <span
                  style={{
                    marginLeft: "auto",
                    font: "11px var(--mono)",
                    color: "var(--fg-faint)",
                  }}
                >
                  request {op.id.slice(0, 8)}
                </span>
              </div>

              <div
                style={{
                  font: "12px var(--mono)",
                  color: "var(--fg-dim)",
                  overflowWrap: "anywhere",
                }}
              >
                {op.artifact}
              </div>

              {op.state === "pending" ? (
                <div
                  style={{ font: "12px var(--sans)", color: "var(--fg-muted)" }}
                >
                  Review this request in Approvals, then execute it as the
                  requesting author.
                </div>
              ) : null}

              {op.attempts.map((a) => (
                <div
                  key={a.number}
                  style={{
                    display: "flex",
                    gap: 8,
                    alignItems: "baseline",
                    font: "12px var(--sans)",
                  }}
                >
                  <span
                    style={{
                      font: "600 9px var(--mono)",
                      letterSpacing: ".08em",
                      color: "var(--fg-faint)",
                      flex: "none",
                    }}
                  >
                    ATTEMPT {a.number}
                  </span>
                  <span style={{ color: "var(--fg-dim)" }}>
                    {a.state}
                    {a.summary ? ` — ${a.summary}` : ""}
                    {a.error ? ` — ${a.error}` : ""}
                  </span>
                </div>
              ))}

              {op.credentials.some((c) => !c.resolved_at) ? (
                <div
                  style={{ font: "11px var(--sans)", color: "var(--fg-faint)" }}
                >
                  Credential cleanup is pending; the service will retry it.
                </div>
              ) : null}

              {op.state === "pending" ? (
                <div>
                  <button
                    disabled={action.isPending}
                    onClick={() =>
                      action.mutate({ id: op.id, verb: "execute" })
                    }
                    style={primaryBtn(action.isPending)}
                  >
                    Execute approved deployment
                  </button>
                </div>
              ) : null}

              {op.state === "failed" ? (
                <div>
                  <button
                    disabled={action.isPending}
                    onClick={() => action.mutate({ id: op.id, verb: "retry" })}
                    style={ghostBtn}
                  >
                    Retry failed deployment
                  </button>
                </div>
              ) : null}

              {op.state === "uncertain" ? (
                <div>
                  <button
                    disabled={action.isPending}
                    onClick={() =>
                      action.mutate({ id: op.id, verb: "reconcile" })
                    }
                    style={ghostBtn}
                  >
                    Check deployment outcome
                  </button>
                </div>
              ) : null}
            </div>
          ))}
      </div>
    </Panel>
  );
}

/** The request form's controls match the one create dialog's fields, so a
 * deployment is asked for the same way everywhere. */
const field: CSSProperties = {
  width: "100%",
  padding: "8px 10px",
  background: "var(--bg)",
  border: "1px solid var(--line-2)",
  borderRadius: 7,
  color: "var(--fg)",
  font: "13px var(--sans)",
  outline: "none",
};

function primaryBtn(busy: boolean): CSSProperties {
  return {
    padding: "7px 14px",
    background: "var(--accent)",
    border: "none",
    borderRadius: 8,
    color: "#fff",
    font: "600 12px var(--sans)",
    cursor: busy ? "default" : "pointer",
    opacity: busy ? 0.6 : 1,
  };
}

const ghostBtn: CSSProperties = {
  padding: "7px 14px",
  background: "transparent",
  border: "1px solid var(--line-2)",
  borderRadius: 8,
  color: "var(--fg-muted)",
  font: "12px var(--sans)",
  cursor: "pointer",
};
