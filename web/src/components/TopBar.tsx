import { Link } from "react-router-dom";
import { setStoredToken } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import type { User } from "../lib/types";

/** TopBar shows where the user is — the design's "novaforge / <project>"
 * breadcrumb — and who they are. */
export function TopBar({
  user,
  onSignedOut,
}: {
  user: User | null;
  onSignedOut: () => void;
}) {
  const w = useWorkspace();
  const path = `${w.org ?? "…"} / ${w.repo ?? "all projects"}`;

  return (
    <header
      style={{
        height: 46,
        flex: "none",
        borderBottom: "1px solid var(--line)",
        display: "flex",
        alignItems: "center",
        gap: 12,
        padding: "0 18px",
      }}
    >
      <span style={{ font: "12px var(--mono)", color: "var(--fg-muted)" }}>
        {path}
      </span>
      <div style={{ flex: 1 }} />
      <Link
        to="/account"
        style={{
          font: "12px var(--sans)",
          color: "var(--fg-dim)",
          display: "flex",
          alignItems: "center",
          gap: 8,
        }}
      >
        <span
          style={{
            width: 22,
            height: 22,
            borderRadius: 99,
            background: "#2f3542",
            display: "grid",
            placeItems: "center",
            font: "600 10px var(--sans)",
            color: "#fff",
          }}
        >
          {(user?.username ?? "?").slice(0, 1).toUpperCase()}
        </span>
        {user?.username ?? "account"}
      </Link>
      <button
        onClick={() => {
          setStoredToken(null);
          onSignedOut();
        }}
        style={{
          background: "transparent",
          border: "1px solid var(--line-2)",
          borderRadius: 7,
          padding: "4px 10px",
          font: "11px var(--sans)",
          color: "var(--fg-muted)",
          cursor: "pointer",
        }}
      >
        Sign out
      </button>
    </header>
  );
}
