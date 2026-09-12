import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { Async, Empty, Page, Panel, PanelHead } from "../components/ui";
import type { PersonalToken, SSHKey, User } from "../lib/types";

/** Account is the design's account screen: the credentials this user holds.
 * Everything here is revocable, which is the point of showing it. */
export function Account() {
  const qc = useQueryClient();

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

  const revokeToken = useMutation({
    mutationFn: (id: string) => api.del(`/api/v1/user/tokens/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["tokens"] }),
  });
  const removeKey = useMutation({
    mutationFn: (id: string) => api.del(`/api/v1/user/ssh-keys/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["ssh-keys"] }),
  });

  return (
    <Page title="Account" subtitle={me.data?.email ?? ""}>
      <div style={{ display: "grid", gap: 14, maxWidth: 760 }}>
        <Panel>
          <PanelHead>PERSONAL ACCESS TOKENS</PanelHead>
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
          <PanelHead>SSH KEYS</PanelHead>
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
