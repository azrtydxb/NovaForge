import { useQuery } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import { Async, Empty, Page, Panel, PanelHead } from "../components/ui";
import type { OrgMember } from "../lib/types";

/** Orgs is the design's admin view: the organization's repositories and its
 * members. Agents appear here alongside people because they are members —
 * with an authority that is a capability grant rather than a token. */
export function Orgs() {
  const w = useWorkspace();

  const members = useQuery({
    queryKey: ["members", w.org],
    queryFn: () =>
      api.get<{ members: OrgMember[] }>(`/api/v1/orgs/${enc(w.org!)}/members`),
    enabled: w.org !== null,
  });

  return (
    <Page title="Org & repositories" subtitle={w.org ?? ""}>
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "minmax(0,1fr) minmax(0,1fr)",
          gap: 14,
        }}
      >
        <Panel>
          <PanelHead>
            REPOSITORIES
            <span style={{ color: "var(--fg-faint)" }}>{w.repos.length}</span>
          </PanelHead>
          {w.repos.length === 0 ? (
            <Empty>No repositories yet.</Empty>
          ) : (
            w.repos.map((r) => (
              <div
                key={r.id}
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: 11,
                  padding: "10px 14px",
                  borderBottom: "1px solid var(--line)",
                }}
              >
                <span style={{ flex: 1, font: "13px var(--sans)" }}>
                  {r.name}
                </span>
                <span
                  style={{ font: "11px var(--mono)", color: "var(--fg-faint)" }}
                >
                  {r.default_branch}
                </span>
              </div>
            ))
          )}
        </Panel>

        <Panel>
          <PanelHead>MEMBERS</PanelHead>
          <Async query={members}>
            {(d) =>
              d.members.length === 0 ? (
                <Empty>No members.</Empty>
              ) : (
                <>
                  {d.members.map((m) => (
                    <div
                      key={m.id}
                      style={{
                        display: "flex",
                        alignItems: "center",
                        gap: 11,
                        padding: "10px 14px",
                        borderBottom: "1px solid var(--line)",
                      }}
                    >
                      <span
                        style={{
                          width: 26,
                          height: 26,
                          borderRadius: 99,
                          background:
                            m.kind === "agent" ? "#33415e" : "#2f3542",
                          display: "grid",
                          placeItems: "center",
                          font: "600 10px var(--sans)",
                          flex: "none",
                        }}
                      >
                        {m.name.slice(0, 1).toUpperCase()}
                      </span>
                      <span style={{ flex: 1 }}>
                        <div style={{ font: "13px var(--sans)" }}>{m.name}</div>
                        <div
                          style={{
                            font: "11px var(--mono)",
                            color: "var(--fg-faint)",
                            marginTop: 2,
                          }}
                        >
                          {m.kind === "agent"
                            ? "agent — capability-scoped"
                            : "human"}
                        </div>
                      </span>
                      <span
                        style={{
                          font: "11px var(--mono)",
                          color: "var(--fg-muted)",
                        }}
                      >
                        {m.role}
                      </span>
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
