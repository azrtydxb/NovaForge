import { useState } from "react";
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
import { Dialog, NewButton } from "../components/Dialog";

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
 * here.
 *
 * Adding a secret is write-only: the value goes to the broker and no screen or
 * endpoint ever returns it. Only an owner or admin may add one; the service
 * refuses anyone else and the dialog shows its answer. */
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

  const [adding, setAdding] = useState(false);
  const add = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post(`/api/v1/orgs/${enc(w.org!)}/secrets`, {
        name: v.name,
        environment: v.environment,
        value: v.value,
      }),
    onSuccess: () => {
      setAdding(false);
      qc.invalidateQueries({ queryKey: ["secrets"] });
    },
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
      actions={
        w.org !== null ? (
          <NewButton
            label="Add secret"
            onClick={() => {
              add.reset();
              setAdding(true);
            }}
          />
        ) : undefined
      }
    >
      {adding ? (
        <Dialog
          title="Add a secret"
          description="A CI job that lists this name under secrets receives it as an environment variable, through a short-lived lease. A staging job gets staging values only; a production job gets production values only on the default branch. The value cannot be read back."
          submitLabel="Store"
          fields={[
            {
              name: "name",
              label: "Name",
              required: true,
              placeholder: "DEPLOY_TOKEN",
              help: "Upper-case letters, numbers and underscores: the variable the job reads.",
            },
            {
              name: "environment",
              label: "Environment",
              type: "select",
              options: ["staging", "production"],
              required: true,
            },
            {
              name: "value",
              label: "Value",
              type: "password",
              required: true,
            },
          ]}
          busy={add.isPending}
          error={add.error}
          onSubmit={(v) => add.mutate(v)}
          onClose={() => setAdding(false)}
        />
      ) : null}
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
