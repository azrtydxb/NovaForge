import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { Async, Empty, Failed, Page, Panel, PanelHead } from "../components/ui";
import { Dialog, NewButton } from "../components/Dialog";
import type { PersonalToken, SSHKey, User } from "../lib/types";

/** Account settings is the design's account artboard: the credentials this
 * user holds and the second factor that guards them. Everything here is
 * revocable, which is the point of showing it. The mutations are the
 * platform's own — a token's secret is returned once, 2FA enrolment is
 * set-up-then-verify — and none of that contract is changed here. */
export function Account() {
  const qc = useQueryClient();
  const [creating, setCreating] = useState<"token" | "key" | "totp" | null>(
    null,
  );
  // A created token's secret is shown once: the platform stores only its hash,
  // so there is no second chance to read it.
  const [issued, setIssued] = useState<string | null>(null);
  const [totp, setTotp] = useState<{ secret: string; uri: string } | null>(
    null,
  );

  const me = useQuery({
    queryKey: ["me"],
    queryFn: () => api.get<User>("/api/v1/user"),
  });
  const tokens = useQuery({
    queryKey: ["tokens"],
    queryFn: () => api.get<{ tokens: PersonalToken[] }>("/api/v1/user/tokens"),
  });
  const keys = useQuery({
    queryKey: ["ssh-keys"],
    queryFn: () => api.get<{ keys: SSHKey[] }>("/api/v1/user/ssh-keys"),
  });

  const createToken = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post<{ token: string }>("/api/v1/user/tokens", {
        name: v.name,
        scopes: (v.scopes ?? "")
          .split(/[\s,]+/)
          .map((x) => x.trim())
          .filter(Boolean),
      }),
    onSuccess: (d) => {
      setCreating(null);
      setIssued(d.token);
      qc.invalidateQueries({ queryKey: ["tokens"] });
    },
  });

  const addKey = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post("/api/v1/user/ssh-keys", {
        title: v.title,
        public_key: v.public_key,
      }),
    onSuccess: () => {
      setCreating(null);
      qc.invalidateQueries({ queryKey: ["ssh-keys"] });
    },
  });

  const beginTotp = useMutation({
    mutationFn: () =>
      api.post<{ secret: string; uri: string }>("/api/v1/user/2fa/setup", {}),
    onSuccess: (d) => {
      setTotp(d);
      setCreating("totp");
    },
  });

  const confirmTotp = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post("/api/v1/user/2fa/verify", { code: v.code }),
    onSuccess: () => {
      setCreating(null);
      setTotp(null);
      // Broadly, not just ["me"]: the signed-in user is read under several
      // keys in this app ("me", "user"), and every one of them must learn
      // that 2FA is now on, or a panel somewhere keeps offering enrolment.
      qc.invalidateQueries();
    },
  });

  const revokeToken = useMutation({
    mutationFn: (id: string) => api.del(`/api/v1/user/tokens/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["tokens"] }),
  });
  const removeKey = useMutation({
    mutationFn: (id: string) => api.del(`/api/v1/user/ssh-keys/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["ssh-keys"] }),
  });

  return (
    <Page
      title="Account settings"
      subtitle={me.data?.email ?? ""}
      actions={
        <div style={{ display: "flex", gap: 8 }}>
          {me.data && !me.data.totp_enabled ? (
            <button
              onClick={() => beginTotp.mutate()}
              disabled={beginTotp.isPending}
              style={{
                padding: "7px 14px",
                background: "transparent",
                border: "1px solid var(--line-2)",
                borderRadius: 8,
                color: "var(--fg-dim)",
                font: "12px var(--sans)",
                cursor: "pointer",
              }}
            >
              Enable two-factor
            </button>
          ) : null}
          <NewButton label="New SSH key" onClick={() => setCreating("key")} />
          <NewButton label="New token" onClick={() => setCreating("token")} />
        </div>
      }
    >
      {creating === "token" ? (
        <Dialog
          title="New personal access token"
          submitLabel="Create"
          fields={[
            {
              name: "name",
              label: "Name",
              required: true,
              placeholder: "nf-cli",
            },
            {
              name: "scopes",
              label: "Scopes",
              placeholder: "repo:read work:write ci:read",
              help: "Space-separated. A token with no scope can read nothing.",
            },
          ]}
          busy={createToken.isPending}
          error={createToken.error}
          onSubmit={(v) => createToken.mutate(v)}
          onClose={() => setCreating(null)}
        />
      ) : null}
      {creating === "key" ? (
        <Dialog
          title="Add an SSH key"
          submitLabel="Add"
          fields={[
            {
              name: "title",
              label: "Title",
              required: true,
              placeholder: "laptop",
            },
            {
              name: "public_key",
              label: "Public key",
              type: "textarea",
              required: true,
              placeholder: "ssh-ed25519 AAAA…",
              help: "The public half only. Pushing over SSH uses this.",
            },
          ]}
          busy={addKey.isPending}
          error={addKey.error}
          onSubmit={(v) => addKey.mutate(v)}
          onClose={() => setCreating(null)}
        />
      ) : null}
      {creating === "totp" && totp ? (
        <Dialog
          title="Enable two-factor authentication"
          submitLabel="Confirm"
          fields={[
            {
              name: "code",
              label: "Code from your authenticator",
              required: true,
              help: `Secret: ${totp.secret}`,
            },
          ]}
          busy={confirmTotp.isPending}
          error={confirmTotp.error}
          onSubmit={(v) => confirmTotp.mutate(v)}
          onClose={() => {
            setCreating(null);
            setTotp(null);
          }}
        />
      ) : null}

      {issued ? (
        <div
          style={{
            marginBottom: 14,
            padding: "12px 14px",
            border: "1px solid #2bb67344",
            background: "rgba(43,182,115,.07)",
            borderRadius: 9,
          }}
        >
          <div style={{ font: "600 12px var(--sans)", color: "var(--ok)" }}>
            Copy this token now — it is not shown again
          </div>
          <code
            style={{
              display: "block",
              marginTop: 7,
              font: "12px var(--mono)",
              color: "var(--fg)",
              wordBreak: "break-all",
            }}
          >
            {issued}
          </code>
          <button
            onClick={() => setIssued(null)}
            style={{
              marginTop: 10,
              padding: "4px 10px",
              border: "1px solid var(--line-2)",
              borderRadius: 7,
              background: "transparent",
              color: "var(--fg-muted)",
              font: "11px var(--sans)",
              cursor: "pointer",
            }}
          >
            Done
          </button>
        </div>
      ) : null}
      {beginTotp.error ? (
        <div style={{ marginBottom: 14 }}>
          <Failed error={beginTotp.error} />
        </div>
      ) : null}
      {revokeToken.error ? <Failed error={revokeToken.error} /> : null}
      {removeKey.error ? <Failed error={removeKey.error} /> : null}

      <div style={{ display: "grid", gap: 14, maxWidth: 760 }}>
        {/* Two-factor is shown as a state first: the reader needs to know
            whether the account is protected before anything else. */}
        <Panel>
          <PanelHead title="Two-factor authentication" />
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: 11,
              padding: "12px 14px",
            }}
          >
            <span style={{ flex: 1, font: "13px var(--sans)" }}>
              {me.data?.totp_enabled
                ? "Enabled. Sign-in asks for an authenticator code."
                : "Not enabled. Sign-in needs only the password."}
            </span>
            <span
              style={{
                font: "600 10px var(--mono)",
                letterSpacing: ".08em",
                color: me.data?.totp_enabled ? "var(--ok)" : "var(--warn)",
              }}
            >
              {me.data?.totp_enabled ? "ON" : "OFF"}
            </span>
          </div>
        </Panel>

        <Panel>
          <PanelHead
            title="Personal access tokens"
            count={tokens.data?.tokens.length ?? 0}
          >
            <NewButtonSubtle
              onClick={() => setCreating("token")}
              label="New token"
            />
          </PanelHead>
          <Async query={tokens}>
            {(d) =>
              d.tokens.length === 0 ? (
                <Empty>No personal access tokens.</Empty>
              ) : (
                <>
                  {d.tokens.map((t) => (
                    <div key={t.id} style={rowStyle}>
                      <span style={{ flex: 1, font: "13px var(--sans)" }}>
                        {t.name}
                      </span>
                      <span
                        style={{
                          font: "11px var(--mono)",
                          color: "var(--fg-muted)",
                        }}
                      >
                        {t.scopes.join(" ")}
                      </span>
                      <button
                        disabled={revokeToken.isPending}
                        onClick={() => revokeToken.mutate(t.id)}
                        style={dangerButton}
                      >
                        Revoke
                      </button>
                    </div>
                  ))}
                </>
              )
            }
          </Async>
        </Panel>

        <Panel>
          <PanelHead title="SSH keys" count={keys.data?.keys.length ?? 0}>
            <NewButtonSubtle
              onClick={() => setCreating("key")}
              label="New SSH key"
            />
          </PanelHead>
          <Async query={keys}>
            {(d) =>
              d.keys.length === 0 ? (
                <Empty>No SSH keys. Add one to push over SSH.</Empty>
              ) : (
                <>
                  {d.keys.map((k) => (
                    <div key={k.id} style={rowStyle}>
                      <span style={{ flex: 1, font: "13px var(--sans)" }}>
                        {k.title}
                      </span>
                      <span
                        style={{
                          font: "11px var(--mono)",
                          color: "var(--fg-muted)",
                        }}
                      >
                        {k.fingerprint}
                      </span>
                      <button
                        disabled={removeKey.isPending}
                        onClick={() => removeKey.mutate(k.id)}
                        style={dangerButton}
                      >
                        Remove
                      </button>
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

/** NewButtonSubtle is a panel-head action: the same verb the page action
 * offers, at the smaller scale a panel header carries. */
function NewButtonSubtle({
  label,
  onClick,
}: {
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      style={{
        padding: "3px 10px",
        borderRadius: 6,
        border: "1px solid var(--line)",
        background: "transparent",
        color: "var(--fg-muted)",
        font: "500 11px var(--sans)",
        cursor: "pointer",
      }}
    >
      {label}
    </button>
  );
}

const rowStyle: React.CSSProperties = {
  display: "flex",
  alignItems: "center",
  gap: 11,
  padding: "10px 14px",
  borderBottom: "1px solid var(--line)",
};

const dangerButton: React.CSSProperties = {
  padding: "4px 10px",
  border: "1px solid #e5534b66",
  borderRadius: 7,
  background: "transparent",
  color: "var(--bad)",
  font: "11px var(--sans)",
  cursor: "pointer",
};
