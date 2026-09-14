import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import {
  Async,
  Empty,
  Failed,
  Page,
  Panel,
  PanelHead,
  StatePill,
} from "../components/ui";
import { Dialog, NewButton } from "../components/Dialog";
import type { McpServer } from "../lib/types";

interface McpTool {
  name: string;
  description: string;
}

/** A pending decision: which server, and whether it is a rejection (which
 * needs a reason) or a revocation (where one is optional). */
type Asking = { server: McpServer; kind: "reject" | "revoke" } | null;

/** Mcp shows MCP in both directions. The surface NovaForge exposes to external
 * agents — Claude Code, Codex, anything that speaks MCP — is the server's own
 * list, so what is shown is exactly what an external client will discover.
 *
 * The other direction is the organization's register of external MCP servers
 * its agents may be offered. Any member may ask for one; only an owner or
 * admin decides. Whether this viewer may decide comes from the server, so the
 * buttons are offered only to someone whose click would be allowed. */
export function Mcp() {
  const w = useWorkspace();
  const qc = useQueryClient();
  const [requesting, setRequesting] = useState(false);
  const [asking, setAsking] = useState<Asking>(null);

  const tools = useQuery({
    queryKey: ["mcp-tools"],
    queryFn: () =>
      api.get<{ tools: McpTool[]; revision: string }>("/api/v1/mcp/tools"),
  });

  const servers = useQuery({
    queryKey: ["mcp-servers", w.org],
    queryFn: () =>
      api.get<{ servers: McpServer[]; can_decide: boolean }>(
        `/api/v1/orgs/${enc(w.org!)}/mcp/servers`,
      ),
    enabled: w.org !== null,
    retry: false,
  });

  const base = () => `/api/v1/orgs/${enc(w.org!)}/mcp/servers`;
  const refresh = () => qc.invalidateQueries({ queryKey: ["mcp-servers"] });

  const request = useMutation({
    mutationFn: (v: Record<string, string>) => api.post<McpServer>(base(), v),
    onSuccess: () => {
      setRequesting(false);
      refresh();
    },
  });

  const decide = useMutation({
    mutationFn: (v: {
      id: string;
      decision: "approve" | "reject";
      reason: string;
    }) =>
      api.post<McpServer>(`${base()}/${enc(v.id)}/decision`, {
        decision: v.decision,
        reason: v.reason,
      }),
    onSuccess: () => {
      setAsking(null);
      refresh();
    },
  });

  const revoke = useMutation({
    mutationFn: (v: { id: string; reason: string }) =>
      api.del<McpServer>(
        `${base()}/${enc(v.id)}?reason=${encodeURIComponent(v.reason)}`,
      ),
    onSuccess: () => {
      setAsking(null);
      refresh();
    },
  });

  return (
    <Page
      title="MCP"
      subtitle="NovaForge exposes its own MCP server, and approves which external ones its agents may use"
      actions={
        w.org !== null ? (
          <NewButton
            label="Request a server"
            onClick={() => {
              request.reset();
              setRequesting(true);
            }}
          />
        ) : undefined
      }
    >
      {requesting ? (
        <Dialog
          title="Request an external MCP server"
          description="The request is recorded as pending. An organization owner or admin decides whether agents may use it."
          submitLabel="Request"
          fields={[
            {
              name: "name",
              label: "Name",
              required: true,
              placeholder: "jira",
              help: "Lowercase letters, digits, '.', '_' or '-'. Unique in this organization.",
            },
            {
              name: "transport",
              label: "Transport",
              type: "select",
              options: ["streamable_http", "stdio"],
              required: true,
            },
            {
              name: "url",
              label: "URL or command",
              required: true,
              placeholder: "https://mcp.example.com/mcp",
              help: "An http(s) endpoint for streamable_http; the launch command for stdio.",
            },
            {
              name: "description",
              label: "What agents would use it for",
              type: "textarea",
            },
          ]}
          busy={request.isPending}
          error={request.error}
          onSubmit={(v) => request.mutate(v)}
          onClose={() => setRequesting(false)}
        />
      ) : null}

      {asking ? (
        <DecisionDialog
          asking={asking}
          busy={decide.isPending || revoke.isPending}
          error={asking.kind === "reject" ? decide.error : revoke.error}
          onSubmit={(reason) =>
            asking.kind === "reject"
              ? decide.mutate({
                  id: asking.server.id,
                  decision: "reject",
                  reason,
                })
              : revoke.mutate({ id: asking.server.id, reason })
          }
          onClose={() => setAsking(null)}
        />
      ) : null}

      <div style={{ display: "grid", gap: 14 }}>
        <Panel>
          <PanelHead>
            EXTERNAL SERVERS
            {servers.data ? (
              <span style={{ color: "var(--fg-faint)" }}>
                {servers.data.servers.length}
              </span>
            ) : null}
            <div style={{ flex: 1 }} />
            {servers.data && !servers.data.can_decide ? (
              <span
                style={{ font: "10px var(--sans)", color: "var(--fg-faint)" }}
              >
                an owner or admin decides
              </span>
            ) : null}
          </PanelHead>
          {w.org === null ? (
            <Empty>No organization selected.</Empty>
          ) : (
            <Async query={servers}>
              {(d) =>
                d.servers.length === 0 ? (
                  <Empty>
                    No external MCP server has been requested for this
                    organization.
                  </Empty>
                ) : (
                  <>
                    {decide.error && !asking ? (
                      <div style={{ padding: 12 }}>
                        <Failed error={decide.error} />
                      </div>
                    ) : null}
                    {d.servers.map((s) => (
                      <ServerRow
                        key={s.id}
                        server={s}
                        canDecide={d.can_decide}
                        busy={decide.isPending}
                        onApprove={() => {
                          decide.reset();
                          decide.mutate({
                            id: s.id,
                            decision: "approve",
                            reason: "",
                          });
                        }}
                        onReject={() => {
                          decide.reset();
                          setAsking({ server: s, kind: "reject" });
                        }}
                        onRevoke={() => {
                          revoke.reset();
                          setAsking({ server: s, kind: "revoke" });
                        }}
                      />
                    ))}
                  </>
                )
              }
            </Async>
          )}
        </Panel>

        <Async query={tools}>
          {(d) => (
            <Panel>
              <PanelHead>
                EXPOSED TOOLS
                <span style={{ color: "var(--fg-faint)" }}>
                  {d.tools.length}
                </span>
                <div style={{ flex: 1 }} />
                <span
                  style={{ font: "10px var(--mono)", color: "var(--fg-faint)" }}
                >
                  spec revision {d.revision}
                </span>
              </PanelHead>
              {d.tools.length === 0 ? (
                <Empty>This deployment exposes no MCP tools.</Empty>
              ) : (
                d.tools.map((t) => (
                  <div
                    key={t.name}
                    style={{
                      display: "flex",
                      gap: 14,
                      padding: "10px 14px",
                      borderBottom: "1px solid var(--line)",
                    }}
                  >
                    <span
                      style={{
                        width: 250,
                        font: "12px var(--mono)",
                        color: "var(--link)",
                        flex: "none",
                      }}
                    >
                      {t.name}
                    </span>
                    <span
                      style={{
                        font: "12px var(--sans)",
                        color: "var(--fg-dim)",
                      }}
                    >
                      {t.description}
                    </span>
                  </div>
                ))
              )}
            </Panel>
          )}
        </Async>
      </div>
    </Page>
  );
}

function ServerRow({
  server: s,
  canDecide,
  busy,
  onApprove,
  onReject,
  onRevoke,
}: {
  server: McpServer;
  canDecide: boolean;
  busy: boolean;
  onApprove: () => void;
  onReject: () => void;
  onRevoke: () => void;
}) {
  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        gap: 11,
        padding: "10px 14px",
        borderBottom: "1px solid var(--line)",
      }}
    >
      <span style={{ flex: 1, minWidth: 0 }}>
        <div style={{ font: "12px var(--mono)" }}>
          {s.name}{" "}
          <span style={{ color: "var(--fg-faint)" }}>· {s.transport}</span>
        </div>
        <div
          style={{
            font: "11px var(--mono)",
            color: "var(--fg-muted)",
            marginTop: 3,
            overflow: "hidden",
            textOverflow: "ellipsis",
            whiteSpace: "nowrap",
          }}
          title={s.url}
        >
          {s.url}
        </div>
        {s.description ? (
          <div
            style={{
              font: "12px var(--sans)",
              color: "var(--fg-dim)",
              marginTop: 3,
            }}
          >
            {s.description}
          </div>
        ) : null}
        {s.decided_at ? (
          <div
            style={{
              font: "10px var(--mono)",
              color: "var(--fg-faint)",
              marginTop: 3,
            }}
          >
            {s.status} {s.decided_at.slice(0, 10)}
            {s.reason ? ` · ${s.reason}` : ""}
          </div>
        ) : null}
      </span>
      <StatePill state={s.status} />
      {canDecide && s.status === "pending" ? (
        <>
          <button
            disabled={busy}
            onClick={onApprove}
            style={actionButton("var(--ok)")}
          >
            Approve
          </button>
          <button onClick={onReject} style={actionButton("var(--bad)")}>
            Reject
          </button>
        </>
      ) : null}
      {canDecide && s.status === "approved" ? (
        <button onClick={onRevoke} style={actionButton("var(--bad)")}>
          Revoke
        </button>
      ) : null}
    </div>
  );
}

function DecisionDialog({
  asking,
  busy,
  error,
  onSubmit,
  onClose,
}: {
  asking: NonNullable<Asking>;
  busy: boolean;
  error: unknown;
  onSubmit: (reason: string) => void;
  onClose: () => void;
}) {
  const reject = asking.kind === "reject";
  return (
    <Dialog
      title={`${reject ? "Reject" : "Revoke"} ${asking.server.name}?`}
      description={
        reject
          ? "The requester sees this reason. A rejected server stays on record."
          : "Agents stop being offered this server. The approval and this revocation both stay on record."
      }
      submitLabel={reject ? "Reject" : "Revoke"}
      fields={[
        {
          name: "reason",
          label: "Reason",
          type: "textarea",
          required: reject,
        },
      ]}
      busy={busy}
      error={error}
      onSubmit={(v) => onSubmit(v.reason ?? "")}
      onClose={onClose}
    />
  );
}

function actionButton(color: string): React.CSSProperties {
  return {
    padding: "4px 10px",
    border: `1px solid ${color}`,
    borderRadius: 7,
    background: "transparent",
    color,
    font: "12px var(--sans)",
    cursor: "pointer",
  };
}
