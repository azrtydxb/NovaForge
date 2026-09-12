import { useState } from "react";
import { NavLink, useLocation } from "react-router-dom";
import { ContextSwitcher } from "./ContextSwitcher";

/** NAV is the design's three-group rail, in its order. The glyphs are the
 * design's own — single characters rather than an icon dependency, which is
 * what lets the rail collapse to 62px without a second set of assets. */
const NAV: {
  label: string;
  items: { glyph: string; label: string; to: string }[];
}[] = [
  {
    label: "BUILD",
    items: [
      { glyph: "⌂", label: "Home", to: "/" },
      { glyph: "W", label: "Work", to: "/work" },
      { glyph: "⇶", label: "Swarm", to: "/swarm" },
      { glyph: "◆", label: "Repositories", to: "/repos" },
      { glyph: "CI", label: "CI", to: "/ci" },
    ],
  },
  {
    label: "GOVERN",
    items: [
      { glyph: "R", label: "Runs", to: "/runs" },
      { glyph: "A", label: "Agents", to: "/agents" },
      { glyph: "!", label: "Exceptions", to: "/exceptions" },
      { glyph: "M", label: "Maintenance", to: "/maintenance" },
      { glyph: "K", label: "Knowledge", to: "/knowledge" },
      { glyph: "G", label: "Graph", to: "/graph" },
    ],
  },
  {
    label: "ADMIN",
    items: [
      { glyph: "O", label: "Org & repos", to: "/orgs" },
      { glyph: "⚿", label: "Secrets", to: "/secrets" },
      { glyph: "⌘", label: "MCP", to: "/mcp" },
      { glyph: "S", label: "Settings", to: "/settings" },
    ],
  },
];

/** exceptionBadge is the count shown on the Exceptions entry. It is passed in
 * rather than fetched here so the rail stays a presentation component and the
 * number has exactly one source. */
export function Rail({ exceptionCount }: { exceptionCount: number | null }) {
  const [hovered, setHovered] = useState(false);
  const [switcherOpen, setSwitcherOpen] = useState(false);
  const open = hovered || switcherOpen;
  const location = useLocation();

  return (
    <nav
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => {
        setHovered(false);
        setSwitcherOpen(false);
      }}
      style={{
        width: open ? "var(--rail-expanded)" : "var(--rail-collapsed)",
        flex: "none",
        borderRight: "1px solid var(--line)",
        padding: "14px 10px",
        display: "flex",
        flexDirection: "column",
        gap: 3,
        transition: "width .18s ease",
        overflow: "hidden",
      }}
    >
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 10,
          padding: "4px 5px 12px",
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
        <span
          style={{
            font: "600 14px var(--sans)",
            opacity: open ? 1 : 0,
            transition: "opacity .15s",
          }}
        >
          NovaForge
        </span>
      </div>

      <ContextSwitcher
        open={switcherOpen}
        railOpen={open}
        onToggle={() => setSwitcherOpen((v) => !v)}
        onPick={() => setSwitcherOpen(false)}
      />

      <div style={{ overflowY: "auto", overflowX: "hidden", flex: 1 }}>
        {NAV.map((group) => (
          <div key={group.label} style={{ marginBottom: 10 }}>
            <div
              style={{
                font: "600 9px var(--sans)",
                letterSpacing: ".12em",
                color: "var(--fg-faint)",
                padding: "10px 8px 4px",
                opacity: open ? 1 : 0,
                transition: "opacity .15s",
                whiteSpace: "nowrap",
              }}
            >
              {group.label}
            </div>
            {group.items.map((item) => {
              const active =
                item.to === "/"
                  ? location.pathname === "/"
                  : location.pathname.startsWith(item.to);
              return (
                <NavLink
                  key={item.to}
                  to={item.to}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: 10,
                    padding: "7px 8px",
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
                    {item.glyph}
                  </span>
                  <span
                    style={{
                      opacity: open ? 1 : 0,
                      transition: "opacity .15s",
                      flex: 1,
                    }}
                  >
                    {item.label}
                  </span>
                  {item.label === "Exceptions" &&
                  exceptionCount !== null &&
                  exceptionCount > 0 ? (
                    <span
                      style={{
                        opacity: open ? 1 : 0,
                        transition: "opacity .15s",
                        background: "var(--bad-bg)",
                        color: "var(--bad)",
                        borderRadius: 99,
                        padding: "1px 6px",
                        font: "600 10px var(--mono)",
                      }}
                    >
                      {exceptionCount}
                    </span>
                  ) : null}
                </NavLink>
              );
            })}
          </div>
        ))}
      </div>
    </nav>
  );
}
