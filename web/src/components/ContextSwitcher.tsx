import { useWorkspace } from "../lib/workspace";

/** ContextSwitcher is the design's project picker at the top of the rail: the
 * current scope, and a dropdown of "All projects" plus each repository. */
export function ContextSwitcher({
  open,
  railOpen,
  onToggle,
  onPick,
}: {
  open: boolean;
  railOpen: boolean;
  onToggle: () => void;
  onPick: () => void;
}) {
  const w = useWorkspace();

  const label = w.repo ?? "All projects";
  const initial = (w.repo ?? w.org ?? "N").slice(0, 1).toUpperCase();
  const color = w.repo ? "var(--link)" : "var(--accent)";

  return (
    <div style={{ position: "relative", margin: "0 0 10px" }}>
      <button
        onClick={onToggle}
        style={{
          display: "flex",
          alignItems: "center",
          gap: 8,
          padding: "6px 8px",
          border: "1px solid var(--line-2)",
          borderRadius: 8,
          cursor: "pointer",
          whiteSpace: "nowrap",
          background: "var(--panel)",
          width: "100%",
          textAlign: "left",
        }}
      >
        <span
          style={{
            width: 18,
            height: 18,
            flex: "none",
            borderRadius: 5,
            background: color,
            display: "grid",
            placeItems: "center",
            font: "700 9px var(--sans)",
            color: "#fff",
          }}
        >
          {initial}
        </span>
        <span
          style={{
            font: "500 12px var(--sans)",
            opacity: railOpen ? 1 : 0,
            transition: "opacity .15s",
            flex: 1,
            overflow: "hidden",
            textOverflow: "ellipsis",
          }}
        >
          {label}
        </span>
        <span
          style={{
            opacity: railOpen ? 1 : 0,
            color: "var(--fg-muted)",
            font: "10px var(--mono)",
          }}
        >
          ▾
        </span>
      </button>

      {open ? (
        <div
          style={{
            position: "absolute",
            top: "calc(100% + 6px)",
            left: 0,
            right: 0,
            background: "var(--panel-2)",
            border: "1px solid var(--line-3)",
            borderRadius: 9,
            padding: 5,
            zIndex: 20,
            maxHeight: 320,
            overflowY: "auto",
          }}
        >
          {w.orgs.length > 1 ? (
            <>
              <div style={sectionLabel}>ORGANIZATION</div>
              {w.orgs.map((o) => (
                <Row
                  key={o.id}
                  initial={o.name.slice(0, 1).toUpperCase()}
                  color="var(--accent)"
                  label={o.name}
                  meta="org"
                  selected={o.name === w.org}
                  onClick={() => {
                    w.setOrg(o.name);
                    onPick();
                  }}
                />
              ))}
              <div style={sectionLabel}>PROJECT</div>
            </>
          ) : null}

          <Row
            initial="N"
            color="var(--accent)"
            label="All projects"
            meta="org"
            selected={w.repo === null}
            onClick={() => {
              w.setRepo(null);
              onPick();
            }}
          />
          {w.repos.map((r) => (
            <Row
              key={r.id}
              initial={r.name.slice(0, 1).toUpperCase()}
              color="var(--link)"
              label={r.name}
              meta={r.default_branch}
              selected={r.name === w.repo}
              onClick={() => {
                w.setRepo(r.name);
                onPick();
              }}
            />
          ))}
          {w.repos.length === 0 && !w.loading ? (
            <div
              style={{
                padding: "8px 9px",
                font: "12px var(--sans)",
                color: "var(--fg-muted)",
              }}
            >
              No repositories yet.
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

const sectionLabel: React.CSSProperties = {
  font: "600 9px var(--sans)",
  letterSpacing: ".12em",
  color: "var(--fg-faint)",
  padding: "8px 9px 4px",
};

function Row({
  initial,
  color,
  label,
  meta,
  selected,
  onClick,
}: {
  initial: string;
  color: string;
  label: string;
  meta: string;
  selected: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      style={{
        display: "flex",
        alignItems: "center",
        gap: 9,
        padding: "7px 9px",
        borderRadius: 7,
        cursor: "pointer",
        width: "100%",
        border: "none",
        textAlign: "left",
        background: selected ? "var(--accent-softer)" : "transparent",
      }}
    >
      <span
        style={{
          width: 18,
          height: 18,
          flex: "none",
          borderRadius: 5,
          background: color,
          display: "grid",
          placeItems: "center",
          font: "700 9px var(--sans)",
          color: "#fff",
        }}
      >
        {initial}
      </span>
      <span style={{ font: "500 12px var(--sans)", flex: 1 }}>{label}</span>
      <span style={{ font: "10px var(--mono)", color: "var(--fg-faint)" }}>
        {meta}
      </span>
    </button>
  );
}
