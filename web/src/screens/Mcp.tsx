import { useQuery } from "@tanstack/react-query";
import { api } from "../lib/api";
import { Async, Empty, Page, Panel, PanelHead } from "../components/ui";

interface McpTool {
  name: string;
  description: string;
}

/** Mcp shows the surface NovaForge exposes to external agents — Claude Code,
 * Codex, anything that speaks MCP. The list is the server's own, so what is
 * shown here is exactly what an external client will discover. */
export function Mcp() {
  const tools = useQuery({
    queryKey: ["mcp-tools"],
    queryFn: () =>
      api.get<{ tools: McpTool[]; revision: string }>("/api/v1/mcp/tools"),
  });

  return (
    <Page
      title="MCP"
      subtitle="NovaForge exposes its own MCP server so external agents can drive it"
    >
      <Async query={tools}>
        {(d) => (
          <Panel>
            <PanelHead>
              EXPOSED TOOLS
              <span style={{ color: "var(--fg-faint)" }}>{d.tools.length}</span>
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
                    style={{ font: "12px var(--sans)", color: "var(--fg-dim)" }}
                  >
                    {t.description}
                  </span>
                </div>
              ))
            )}
          </Panel>
        )}
      </Async>
    </Page>
  );
}
