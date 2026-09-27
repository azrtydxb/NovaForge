import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, enc } from "../lib/api";
import { Panel, PanelHead, Failed, Empty, StatePill } from "../components/ui";

const newRequestID = () => {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6]! & 15) | 64; bytes[8] = (bytes[8]! & 63) | 128;
  const h = Array.from(bytes, b => b.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
};

type Target = { name: string; environment: string; revision: string };
type Deployment = {
  id: string; run_id: string; target: string; artifact: string; state: string;
  attempts: { number: number; state: string; error: string; summary: string }[];
  credentials: { attempt: number; resolved_at: string }[];
};

export function Deployments({ org, repo, runID }: { org: string; repo: string; runID: string }) {
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}`;
  const qc = useQueryClient();
  const [target, setTarget] = useState("");
  const [artifact, setArtifact] = useState("");
  const [requestID, setRequestID] = useState(() => newRequestID());
  const targets = useQuery({ queryKey: ["deployment-targets", org, repo], queryFn: () => api.get<{ targets: Target[] }>(`${base}/deployment-targets`), retry: false });
  const operations = useQuery({ queryKey: ["deployments", org, repo], queryFn: () => api.get<{ operations: Deployment[] }>(`${base}/deployments`), enabled: !!targets.data, refetchInterval: 5000 });
  const refresh = () => { void qc.invalidateQueries({ queryKey: ["deployments", org, repo] }); void qc.invalidateQueries({ queryKey: ["approvals", org] }); };
  const request = useMutation({
    mutationFn: () => api.post<Deployment>(`${base}/deployments`, { id: requestID, run_id: runID, target, artifact }),
    onSuccess: () => { setRequestID(newRequestID()); refresh(); },
  });
  const action = useMutation({ mutationFn: ({ id, verb }: { id: string; verb: string }) => api.post<Deployment>(`${base}/deployments/${enc(id)}/${verb}`, {}), onSettled: refresh });
  const unavailable = targets.error instanceof ApiError && [501, 503].includes(targets.error.status);
  return <Panel style={{ marginBottom: 14 }}>
    <PanelHead>DEPLOYMENTS</PanelHead>
    <div style={{ padding: 14, display: "grid", gap: 12 }}>
      {unavailable ? <Empty>No deployment targets are configured in this installation.</Empty> : targets.error ? <Failed error={targets.error} /> : null}
      {targets.data?.targets.length === 0 ? <Empty>No deployment targets are configured for this repository.</Empty> : null}
      {!!targets.data?.targets.length && <>
        <p>Request deployment of an immutable image. An independent reviewer must approve the exact target and artifact before execution.</p>
        <label>Target <select value={target} onChange={e => { setTarget(e.target.value); setRequestID(newRequestID()); }}>
          <option value="">Choose a target</option>
          {targets.data.targets.map(t => <option key={t.name} value={t.name}>{t.name} · {t.environment}</option>)}
        </select></label>
        <label>Image digest <input value={artifact} placeholder="sha256:…" onChange={e => { setArtifact(e.target.value); setRequestID(newRequestID()); }} /></label>
        <button disabled={!target || !/^sha256:[a-f0-9]{64}$/.test(artifact) || request.isPending} onClick={() => request.mutate()}>Request deployment approval</button>
      </>}
      {request.error ? <Failed error={request.error} /> : null}
      {action.error ? <Failed error={action.error} /> : null}
      {operations.error ? <Failed error={operations.error} /> : null}
      {operations.data?.operations.filter(op => op.run_id === runID).map(op => <div key={op.id} style={{ borderTop: "1px solid var(--line)", paddingTop: 12 }}>
        <strong>{op.target}</strong> <StatePill state={op.state} />
        <p style={{ overflowWrap: "anywhere" }}>{op.artifact}</p>
        <small>Request {op.id}</small>
        {op.state === "pending" && <p>Review this request in Approvals, then execute it as the requesting author.</p>}
        {op.attempts.map(a => <p key={a.number}>Attempt {a.number}: {a.state}{a.summary ? ` — ${a.summary}` : ""}{a.error ? ` — ${a.error}` : ""}</p>)}
        {op.credentials.some(c => !c.resolved_at) && <p>Credential cleanup is pending; the service will retry it.</p>}
        {op.state === "pending" && <button disabled={action.isPending} onClick={() => action.mutate({ id: op.id, verb: "execute" })}>Execute approved deployment</button>}
        {op.state === "failed" && <button disabled={action.isPending} onClick={() => action.mutate({ id: op.id, verb: "retry" })}>Retry failed deployment</button>}
        {op.state === "uncertain" && <button disabled={action.isPending} onClick={() => action.mutate({ id: op.id, verb: "reconcile" })}>Check deployment outcome</button>}
      </div>)}
    </div>
  </Panel>;
}
