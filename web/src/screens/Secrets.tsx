import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import {
  Async,
  Empty,
  Page,
  Panel,
  PanelHead,
  StatePill,
} from "../components/ui";

interface Lease {
  id: string;
  secret_name: string;
  run_id: string;
  state: string;
  expires_at: string;
}

interface SecretRef {
  name: string;
  environment: string;
}

/** Secrets never shows a secret's value — the platform brokers short-lived
 * credentials to runs and this screen shows the brokering, not the material.
 * A lease is revocable while it is live, which is the one action worth having
 * here. */
export function Secrets() {
  const w = useWorkspace();
  const qc = useQueryClient();

  const secrets = useQuery({
    queryKey: ["secrets", w.org],
    queryFn: () =>
      api.get<{ secrets: SecretRef[] }>(`/api/v1/orgs/${enc(w.org!)}/secrets`),
    enabled: w.org !== null,
  });

  const leases = useQuery({
    queryKey: ["leases", w.org],
    queryFn: () =>
      api.get<{ leases: Lease[] }>(`/api/v1/orgs/${enc(w.org!)}/leases`),
    enabled: w.org !== null,
  });

  const revoke = useMutation({
    mutationFn: (id: string) =>
      api.del(`/api/v1/orgs/${enc(w.org!)}/leases/${enc(id)}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["leases"] }),
  });

  return (
    <Page
      title="Secrets"
      subtitle="Credentials are brokered to a run for a bounded time, never handed to an agent"
    >
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "minmax(0,1fr) minmax(0,1.3fr)",
          gap: 14,
        }}
      >
        <Panel>
          <PanelHead>SECRETS</PanelHead>
          <Async query={secrets}>
            {(d) =>
              d.secrets.length === 0 ? (
                <Empty>No secrets are registered for this organization.</Empty>
              ) : (
                <>
                  {d.secrets.map((s) => (
                    <div
                      key={`${s.environment}/${s.name}`}
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: 11,
                        padding: "10px 14px",
                        borderBottom: "1px solid var(--line)",
                      }}
                    >
                      <span style={{ flex: 1, font: "12px var(--mono)" }}>
                        {s.name}
                      </span>
                      <StatePill
                        state={
                          s.environment === "production" ? "failed" : "review"
                        }
                      />
                      <span
                        style={{
                          font: "11px var(--mono)",
                          color: "var(--fg-muted)",
                        }}
                      >
                        {s.environment}
                      </span>
                    </div>
                  ))}
                </>
              )
            }
          </Async>
        </Panel>

        <Panel>
          <PanelHead>ACTIVE LEASES</PanelHead>
          <Async query={leases}>
            {(d) =>
              d.leases.length === 0 ? (
                <Empty>No credential is leased right now.</Empty>
              ) : (
                <>
                  {d.leases.map((l) => (
                    <div
                      key={l.id}
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: 11,
                        padding: "10px 14px",
                        borderBottom: "1px solid var(--line)",
                      }}
                    >
                      <span style={{ flex: 1 }}>
                        <div style={{ font: "12px var(--mono)" }}>
                          {l.secret_name}
                        </div>
                        <div
                          style={{
                            font: "10px var(--mono)",
                            color: "var(--fg-faint)",
                            marginTop: 3,
                          }}
                        >
                          run {l.run_id.slice(0, 8)} · expires{" "}
                          {l.expires_at.slice(11, 19)}
                        </div>
                      </span>
                      <StatePill state={l.state} />
                      {l.state === "issued" ? (
                        <button
                          onClick={() => revoke.mutate(l.id)}
                          style={{
                            padding: "4px 10px",
                            border: "1px solid #e5534b66",
                            borderRadius: 7,
                            background: "transparent",
                            color: "var(--bad)",
                            font: "11px var(--sans)",
                            cursor: "pointer",
                          }}
                        >
                          Revoke
                        </button>
                      ) : null}
                    </div>
                  ))}
                </>
              )
            }
          </Async>
        </Panel>
      </div>
    </Page>
  );
}
