import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, api, enc, setStoredToken } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import { Failed } from "./ui";
import type { Repo, User } from "../lib/types";

/** The redesign's header: a command palette ("Search or jump to…", ⌘K), the
 * count of agents currently working, a New menu (repository, work item, run,
 * agent), the theme toggle, Inbox with its unread badge, and the account
 * menu. Everything is reachable from the keyboard, which is the point of the
 * redesign: g h home, g i inbox, ⌘K search, c new. */

export function TopBar({
  user,
  onSignedOut,
}: {
  user: User | null;
  onSignedOut: () => void;
}) {
  const w = useWorkspace();
  const navigate = useNavigate();
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [newOpen, setNewOpen] = useState(false);
  const [accountOpen, setAccountOpen] = useState(false);

  // The unread count is the same number the Inbox screen shows; when the
  // deployment has no notifications endpoint the badge is simply absent
  // rather than a invented zero.
  const inbox = useQuery({
    queryKey: ["inbox-unread"],
    queryFn: () => api.get<{ unread: number }>("/api/v1/inbox/unread"),
    refetchInterval: 30_000,
    retry: false,
  });
  const hasInbox = !(inbox.error instanceof ApiError);

  const agentsWorking = useQuery({
    queryKey: ["agents-working", w.org],
    queryFn: () =>
      api.get<{ total: number }>(`/api/v1/orgs/${enc(w.org!)}/agents/stats`),
    enabled: w.org !== null,
    refetchInterval: 30_000,
  });

  const qc = useQueryClient();
  const logout = useMutation({
    mutationFn: () => api.post("/api/v1/auth/logout", {}),
    onSuccess: () => {
      setStoredToken(null);
      qc.clear();
      onSignedOut();
    },
  });

  const [pendingG, setPendingG] = useState<string | null>(null);

  // One global keymap: the redesign's shortcuts are worth nothing if they
  // only work while a text field is not focused, so every handler here
  // checks for that itself.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      const typing =
        target &&
        (target.tagName === "INPUT" ||
          target.tagName === "TEXTAREA" ||
          target.isContentEditable);
      if (typing) return;
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteOpen((v) => !v);
      } else if (e.key === "c" && !e.metaKey && !e.ctrlKey && !e.altKey) {
        setNewOpen((v) => !v);
      } else if (e.key === "g") {
        setPendingG(e.key);
      } else if (pendingG === "g" && !typing) {
        const map: Record<string, string> = { h: "/", i: "/inbox" };
        const to = map[e.key];
        setPendingG(null);
        if (to) {
          e.preventDefault();
          navigate(to);
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [navigate, pendingG]);

  return (
    <header
      style={{
        height: 46,
        flex: "none",
        borderBottom: "1px solid var(--line)",
        display: "flex",
        alignItems: "center",
        gap: 10,
        padding: "0 14px",
      }}
    >
      <button
        onClick={() => setPaletteOpen(true)}
        style={{
          display: "flex",
          alignItems: "center",
          gap: 8,
          background: "var(--panel-2)",
          border: "1px solid var(--line)",
          borderRadius: 7,
          padding: "5px 10px",
          font: "12px var(--sans)",
          color: "var(--fg-muted)",
          cursor: "pointer",
          minWidth: 260,
        }}
      >
        <span aria-hidden>⌕</span>
        Search or jump to…
        <span
          style={{
            marginLeft: "auto",
            font: "10px var(--mono)",
            border: "1px solid var(--line-2)",
            borderRadius: 5,
            padding: "0 4px",
            color: "var(--fg-faint)",
          }}
        >
          ⌘K
        </span>
      </button>

      {w.org !== null && (agentsWorking.data?.total ?? 0) > 0 ? (
        <Linkish onClick={() => navigate("/agents")}>
          <span
            style={{
              width: 7,
              height: 7,
              borderRadius: 99,
              background: "var(--ok)",
              animation: "nfpulse 2s infinite",
            }}
          />
          {agentsWorking.data!.total} agents working
        </Linkish>
      ) : null}

      <div style={{ flex: 1 }} />

      <div style={{ position: "relative" }}>
        <button
          onClick={() => setNewOpen((v) => !v)}
          style={{
            display: "flex",
            alignItems: "center",
            gap: 6,
            background: "var(--accent)",
            border: 0,
            borderRadius: 7,
            padding: "5px 12px",
            font: "600 12px var(--sans)",
            color: "#fff",
            cursor: "pointer",
          }}
          title="Create repository, work item, run or agent (c)"
        >
          New
        </button>
        {newOpen ? (
          <NewMenu
            onClose={() => setNewOpen(false)}
            onPick={(to) => {
              setNewOpen(false);
              navigate(to);
            }}
          />
        ) : null}
      </div>

      <button
        onClick={() => {
          const next =
            document.documentElement.getAttribute("data-theme") === "light"
              ? "dark"
              : "light";
          document.documentElement.setAttribute("data-theme", next);
          try {
            localStorage.setItem("nf-theme", next);
          } catch {}
        }}
        title="Switch theme"
        style={iconBtn}
      >
        ◐
      </button>

      {hasInbox ? (
        <Linkish onClick={() => navigate("/inbox")} title="Inbox (g i)">
          <span aria-hidden>✉</span>
          {(inbox.data?.unread ?? 0) > 0 ? (
            <span
              style={{
                background: "var(--bad-bg)",
                color: "var(--bad)",
                borderRadius: 99,
                padding: "1px 6px",
                font: "600 10px var(--mono)",
              }}
            >
              {inbox.data!.unread}
            </span>
          ) : null}
        </Linkish>
      ) : null}

      <div style={{ position: "relative" }}>
        <button
          onClick={() => setAccountOpen((v) => !v)}
          style={{ ...iconBtn, width: "auto", gap: 6, padding: "4px 8px" }}
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
          <span style={{ font: "12px var(--sans)", color: "var(--fg-dim)" }}>
            {user?.username}
          </span>
        </button>
        {accountOpen ? (
          <div style={menu} onMouseLeave={() => setAccountOpen(false)}>
            <MenuItem
              onClick={() => {
                setAccountOpen(false);
                navigate("/account");
              }}
            >
              Account settings
            </MenuItem>
            <MenuItem
              onClick={() => {
                setAccountOpen(false);
                navigate("/profile");
              }}
            >
              Your profile
            </MenuItem>
            <MenuItem
              onClick={() => {
                setAccountOpen(false);
                logout.mutate();
              }}
            >
              {logout.isPending ? "Signing out…" : "Sign out"}
            </MenuItem>
          </div>
        ) : null}
      </div>

      {logout.error ? <Failed error={logout.error} /> : null}

      {paletteOpen ? (
        <CommandPalette onClose={() => setPaletteOpen(false)} />
      ) : null}
    </header>
  );
}

/** CommandPalette is the ⌘K overlay. It jumps to screens, filters this
 * organization's repositories by name, and — when a repository is selected in
 * the workspace — runs the platform's semantic code search. It never shows
 * invented results: sections with no endpoint in this deployment are absent
 * rather than empty. */
function CommandPalette({ onClose }: { onClose: () => void }) {
  const w = useWorkspace();
  const navigate = useNavigate();
  const [q, setQ] = useState("");
  const box = useRef<HTMLDivElement>(null);

  const repos = useQuery({
    queryKey: ["repos", w.org],
    queryFn: () => api.get<Repo[]>(`/api/v1/orgs/${enc(w.org!)}/repos`),
    enabled: w.org !== null,
  });

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const ql = q.toLowerCase();
  const nav: { label: string; to: string }[] = [
    { label: "Home", to: "/" },
    { label: "Inbox", to: "/inbox" },
    { label: "Repositories", to: "/repos" },
    { label: "Work", to: "/work" },
    { label: "Runs", to: "/runs" },
    { label: "CI", to: "/ci" },
    { label: "Runners", to: "/runners" },
    { label: "Deployments", to: "/deployments" },
    { label: "Agents", to: "/agents" },
    { label: "Swarms", to: "/swarm" },
    { label: "Rules & gates", to: "/rules" },
    { label: "Maintenance", to: "/maintenance" },
    { label: "Knowledge", to: "/knowledge" },
    { label: "Code graph", to: "/graph" },
    { label: "Insights", to: "/insights" },
    { label: "People & teams", to: "/orgs" },
    { label: "Secrets & leases", to: "/secrets" },
    { label: "MCP servers", to: "/mcp" },
    { label: "Account settings", to: "/account" },
  ];
  const navHits = q
    ? nav.filter((n) => n.label.toLowerCase().includes(ql))
    : nav.slice(0, 8);
  const repoHits =
    q && repos.data
      ? repos.data.filter((r) => r.name.toLowerCase().includes(ql)).slice(0, 6)
      : (repos.data ?? []).slice(0, 4);

  return (
    <div
      onClick={onClose}
      style={{
        position: "fixed",
        inset: 0,
        background: "rgba(0,0,0,.55)",
        display: "grid",
        placeItems: "start center",
        paddingTop: 90,
        zIndex: 50,
      }}
    >
      <div
        ref={box}
        onClick={(e) => e.stopPropagation()}
        style={{
          width: 620,
          maxWidth: "92vw",
          background: "var(--panel)",
          border: "1px solid var(--line-3)",
          borderRadius: 12,
          overflow: "hidden",
          boxShadow: "0 18px 60px rgba(0,0,0,.5)",
        }}
      >
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 8,
            padding: "10px 14px",
            borderBottom: "1px solid var(--line)",
          }}
        >
          <span style={{ color: "var(--fg-muted)" }}>⌕</span>
          <input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search code, work, runs and agents…"
            style={{
              flex: 1,
              background: "transparent",
              border: 0,
              outline: "none",
              color: "var(--fg)",
              font: "14px var(--sans)",
            }}
          />
          {w.repo ? (
            <button
              onClick={() => undefined}
              style={{
                font: "11px var(--mono)",
                background: "var(--accent-soft)",
                color: "var(--link)",
                border: 0,
                borderRadius: 6,
                padding: "3px 8px",
                cursor: "pointer",
              }}
            >
              in {w.repo}
            </button>
          ) : null}
        </div>
        <div style={{ maxHeight: 420, overflowY: "auto" }}>
          {repoHits.length > 0 ? (
            <PaletteSection label="Repositories">
              {repoHits.map((r) => (
                <PaletteRow
                  key={r.name}
                  onClick={() => {
                    onClose();
                    navigate(`/repos/${r.name}`);
                  }}
                >
                  ◆ {r.name}
                  <span
                    style={{
                      color: "var(--fg-faint)",
                      font: "11px var(--mono)",
                    }}
                  >
                    {r.default_branch}
                  </span>
                </PaletteRow>
              ))}
            </PaletteSection>
          ) : null}
          {navHits.length > 0 ? (
            <PaletteSection label="Go to">
              {navHits.map((n) => (
                <PaletteRow
                  key={n.to}
                  onClick={() => {
                    onClose();
                    navigate(n.to);
                  }}
                >
                  → {n.label}
                </PaletteRow>
              ))}
            </PaletteSection>
          ) : null}
          {q && w.repo ? (
            <PaletteSection label={`Code in ${w.repo}`}>
              <PaletteRow
                onClick={() => {
                  onClose();
                  navigate(
                    `/repos/${w.repo}/search?q=${encodeURIComponent(q)}`,
                  );
                }}
              >
                ⌕ Search “{q}” in {w.repo} (semantic)
              </PaletteRow>
            </PaletteSection>
          ) : null}
        </div>
        <div
          style={{
            borderTop: "1px solid var(--line)",
            padding: "6px 14px",
            font: "11px var(--mono)",
            color: "var(--fg-faint)",
          }}
        >
          ⏎ open · esc close
        </div>
      </div>
    </div>
  );
}

function NewMenu({
  onClose,
  onPick,
}: {
  onClose: () => void;
  onPick: (to: string) => void;
}) {
  const items: { label: string; hint: string; to: string }[] = [
    { label: "New repository", hint: "import or create", to: "/repos" },
    { label: "New work item", hint: "in the selected repo", to: "/work" },
    { label: "New engineering run", hint: "propose a change", to: "/runs" },
    { label: "New agent", hint: "define an agent role", to: "/agents" },
  ];
  return (
    <div style={{ ...menu, width: 240 }} onMouseLeave={onClose}>
      {items.map((i) => (
        <MenuItem key={i.to} onClick={() => onPick(i.to)}>
          <span>{i.label}</span>
          <span style={{ color: "var(--fg-faint)", font: "10px var(--mono)" }}>
            {i.hint}
          </span>
        </MenuItem>
      ))}
    </div>
  );
}

const iconBtn: React.CSSProperties = {
  background: "transparent",
  border: 0,
  borderRadius: 7,
  padding: "5px 8px",
  font: "14px var(--sans)",
  color: "var(--fg-dim)",
  cursor: "pointer",
  display: "flex",
  alignItems: "center",
};

const menu: React.CSSProperties = {
  position: "absolute",
  right: 0,
  top: "calc(100% + 6px)",
  background: "var(--panel)",
  border: "1px solid var(--line-3)",
  borderRadius: 9,
  minWidth: 190,
  zIndex: 40,
  overflow: "hidden",
  boxShadow: "0 12px 40px rgba(0,0,0,.45)",
};

function MenuItem({
  children,
  onClick,
}: {
  children: React.ReactNode;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      style={{
        display: "flex",
        width: "100%",
        alignItems: "center",
        justifyContent: "space-between",
        gap: 10,
        background: "transparent",
        border: 0,
        borderBottom: "1px solid var(--line)",
        padding: "9px 12px",
        font: "12px var(--sans)",
        color: "var(--fg-dim)",
        cursor: "pointer",
        textAlign: "left",
      }}
    >
      {children}
    </button>
  );
}

function Linkish({
  children,
  onClick,
  title,
}: {
  children: React.ReactNode;
  onClick: () => void;
  title?: string;
}) {
  return (
    <button
      onClick={onClick}
      title={title}
      style={{
        display: "flex",
        alignItems: "center",
        gap: 6,
        background: "transparent",
        border: 0,
        borderRadius: 7,
        padding: "4px 8px",
        font: "12px var(--sans)",
        color: "var(--fg-dim)",
        cursor: "pointer",
      }}
    >
      {children}
    </button>
  );
}

function PaletteSection({
  label,
  children,
}: {
  label: string;
  children?: React.ReactNode;
}) {
  return (
    <div style={{ padding: "6px 0" }}>
      <div
        style={{
          font: "600 9px var(--sans)",
          letterSpacing: ".12em",
          color: "var(--fg-faint)",
          padding: "4px 14px",
        }}
      >
        {label}
        {children}
      </div>
    </div>
  );
}

function PaletteRow({
  children,
  onClick,
}: {
  children: React.ReactNode;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      style={{
        display: "flex",
        width: "100%",
        alignItems: "center",
        gap: 8,
        background: "transparent",
        border: 0,
        padding: "8px 14px",
        font: "13px var(--sans)",
        color: "var(--fg-dim)",
        cursor: "pointer",
        textAlign: "left",
      }}
    >
      {children}
    </button>
  );
}
