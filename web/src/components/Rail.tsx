import { useState } from "react";
import { NavLink, useLocation } from "react-router-dom";
import { ContextSwitcher } from "./ContextSwitcher";

/** NAV is the redesign's five-group rail, in its order. The glyphs are single
 * characters rather than an icon dependency, which is what keeps the rail an
 * asset-free component. Counts arrive as props — the rail is a presentation
 * component, and each badge has exactly one source (the same query the screen
 * it points at reads). */
const NAV: {
  label: string;
  items: {
    glyph: string;
    label: string;
    to: string;
    badge?: number | null;
  }[][];
}[] = [
  {
    label: "YOU",
    items: [
      [
        { glyph: "⌂", label: "Home", to: "/" },
        { glyph: "✉", label: "Inbox", to: "/inbox", badge: null },
      ],
    ],
  },
  {
    label: "BUILD",
    items: [
      [
        { glyph: "◆", label: "Repositories", to: "/repos" },
        { glyph: "W", label: "Work", to: "/work" },
        { glyph: "⇶", label: "Runs", to: "/runs" },
      ],
      [
        { glyph: "CI", label: "CI", to: "/ci" },
        { glyph: "⚙", label: "Runners", to: "/runners" },
        { glyph: "▲", label: "Deployments", to: "/deployments" },
      ],
    ],
  },
  {
    label: "AGENTS",
    items: [
      [
        { glyph: "A", label: "Agents", to: "/agents" },
        { glyph: "≋", label: "Swarms", to: "/swarm" },
      ],
    ],
  },
  {
    label: "GOVERN",
    items: [
      [
        { glyph: "R", label: "Rules & gates", to: "/rules" },
        { glyph: "M", label: "Maintenance", to: "/maintenance" },
      ],
      [
        { glyph: "K", label: "Knowledge", to: "/knowledge" },
        { glyph: "G", label: "Code graph", to: "/graph" },
        { glyph: "◈", label: "Insights", to: "/insights" },
      ],
    ],
  },
  {
    label: "ORG",
    items: [
      [
        { glyph: "O", label: "People & teams", to: "/orgs" },
        { glyph: "⚿", label: "Secrets & leases", to: "/secrets" },
        { glyph: "⌘", label: "MCP servers", to: "/mcp" },
      ],
    ],
  },
];

/** Rail is the redesign's expanded sidebar. The old rail collapsed to icons on
 * hover; the redesign keeps it open — the sections and counts ARE the
 * navigation, and a hover-to-reveal rail hid the very counts that make it
 * useful. Pinned repositories come from the workspace's own org query. */
export function Rail({
  inboxCount,
  workCount,
  runCount,
  deploymentCount,
  agentCount,
  maintenanceCount,
  pinned,
}: {
  inboxCount: number | null;
  workCount: number | null;
  runCount: number | null;
  deploymentCount: number | null;
  agentCount: number | null;
  maintenanceCount: number | null;
  pinned: { name: string }[];
}) {
  const [switcherOpen, setSwitcherOpen] = useState(false);
  const location = useLocation();
  const badges: Record<string, number | null> = {
    "/inbox": inboxCount,
    "/work": workCount,
    "/runs": runCount,
    "/deployments": deploymentCount,
    "/agents": agentCount,
    "/maintenance": maintenanceCount,
  };

  return (
    <nav
      style={{
        width: "var(--rail-expanded)",
        flex: "none",
        borderRight: "1px solid var(--line)",
        padding: "12px 10px",
        display: "flex",
        flexDirection: "column",
        gap: 3,
        overflow: "hidden",
      }}
    >
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 10,
          padding: "4px 5px 10px",
          whiteSpace: "nowrap",
        }}
      >
        <div
          style={{
            width: 26,
            height: 26,
            background: "var(--accent)",
            borderRadius: 6,
            display: "grid",
            placeItems: "center",
            font: "700 13px var(--sans)",
            color: "#fff",
            flex: "none",
          }}
        >
          N
        </div>
        <span style={{ font: "600 14px var(--sans)", letterSpacing: ".02em" }}>
          NOVAFORGE
        </span>
        <span
          title="Press g then h to go home"
          style={{
            font: "10px var(--mono)",
            color: "var(--fg-faint)",
            marginLeft: "auto",
          }}
        >
          g h
        </span>
      </div>

      <ContextSwitcher
        open={switcherOpen}
        railOpen
        onToggle={() => setSwitcherOpen((v) => !v)}
        onPick={() => setSwitcherOpen(false)}
      />

      <div style={{ overflowY: "auto", overflowX: "hidden", flex: 1 }}>
        {NAV.map((group) => (
          <div key={group.label} style={{ marginBottom: 8 }}>
            {group.items.map(
              (
                rows: { glyph: string; label: string; to: string }[],
                i: number,
              ) => (
                <div key={i} style={{ marginBottom: rows.length ? 2 : 0 }}>
                  {i === 0 ? (
                    <div
                      style={{
                        font: "600 9px var(--sans)",
                        letterSpacing: ".12em",
                        color: "var(--fg-faint)",
                        padding: "10px 8px 4px",
                        whiteSpace: "nowrap",
                      }}
                    >
                      {group.label}
                    </div>
                  ) : null}
                  {rows.map(
                    (item: {
                      glyph: string;
                      label: string;
                      to: string;
                      badge?: number | null;
                    }) => {
                      const active =
                        item.to === "/"
                          ? location.pathname === "/"
                          : location.pathname.startsWith(item.to);
                      const badge = item.badge ?? badges[item.to] ?? null;
                      return (
                        <NavLink
                          key={item.to}
                          to={item.to}
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: 10,
                            padding: "6px 8px",
                            borderRadius: 7,
                            whiteSpace: "nowrap",
                            background: active
                              ? "var(--accent-soft)"
                              : "transparent",
                            color: active ? "#fff" : "var(--fg-muted)",
                            font: "500 13px var(--sans)",
                          }}
                        >
                          <span
                            style={{
                              width: 18,
                              flex: "none",
                              textAlign: "center",
                              font: "600 11px var(--mono)",
                            }}
                          >
                            {item.glyph}
                          </span>
                          <span style={{ flex: 1 }}>{item.label}</span>
                          {badge !== null && badge > 0 ? (
                            <span
                              style={{
                                background: active
                                  ? "var(--accent)"
                                  : "var(--line-2)",
                                color: "#fff",
                                borderRadius: 99,
                                padding: "1px 6px",
                                font: "600 10px var(--mono)",
                              }}
                            >
                              {badge}
                            </span>
                          ) : null}
                        </NavLink>
                      );
                    },
                  )}
                </div>
              ),
            )}
          </div>
        ))}

        {pinned.length > 0 ? (
          <div style={{ marginBottom: 8 }}>
            <div
              style={{
                font: "600 9px var(--sans)",
                letterSpacing: ".12em",
                color: "var(--fg-faint)",
                padding: "10px 8px 4px",
                whiteSpace: "nowrap",
              }}
            >
              PINNED REPOS
            </div>
            {pinned.slice(0, 5).map((r) => {
              const to = `/repos/${r.name}`;
              const active = location.pathname === to;
              return (
                <NavLink
                  key={r.name}
                  to={to}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: 10,
                    padding: "6px 8px",
                    borderRadius: 7,
                    whiteSpace: "nowrap",
                    background: active ? "var(--accent-soft)" : "transparent",
                    color: active ? "#fff" : "var(--fg-muted)",
                    font: "500 13px var(--sans)",
                  }}
                >
                  <span
                    style={{
                      width: 18,
                      flex: "none",
                      textAlign: "center",
                      font: "600 11px var(--mono)",
                    }}
                  >
                    ◆
                  </span>
                  <span
                    style={{
                      flex: 1,
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                    }}
                  >
                    {r.name}
                  </span>
                </NavLink>
              );
            })}
          </div>
        ) : null}
      </div>
    </nav>
  );
}
