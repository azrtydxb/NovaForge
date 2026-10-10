import { useQuery } from "@tanstack/react-query";
import { createContext, useContext, type CSSProperties } from "react";
import { api, enc } from "../../lib/api";
import { Empty } from "../../components/ui";
import type { Ref, Repo } from "../../lib/types";

/** Everything under /repos/:repo renders inside this context: the
 * organization and repository it is scoped to, the repository's own record,
 * and whether this person may administer it. RepoPage decides that once so
 * no tab re-derives the platform's answer differently. */
export interface RepoScope {
  org: string;
  repo: string;
  /** The repository's own record. Tabs that need only the default branch
   * read `repo` and wait; the workspace list is not a substitute because a
   * URL can name a repository the remembered workspace does not. */
  repoRecord: Repo | undefined;
  /** An owner or admin of the owning organization, the same role
   * git-platform enforces on every administrative write. */
  canAdminister: boolean;
}

export const RepoScopeCtx = createContext<RepoScope | null>(null);

export function useRepoScope(): RepoScope {
  const v = useContext(RepoScopeCtx);
  if (!v) throw new Error("repo screens used outside RepoPage");
  return v;
}

/** useRefs loads a repository's branches and tags. The two travel together
 * everywhere a ref is chosen — the branch picker, compare, releases — so
 * both are fetched here under one hook and a caller never shows branches
 * without the tags that carry releases. */
export function useRefs(org: string, repo: string) {
  const branches = useQuery({
    queryKey: ["branches", org, repo],
    queryFn: () =>
      api.get<{ refs: Ref[] }>(
        `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}/branches`,
      ),
  });
  const tags = useQuery({
    queryKey: ["tags", org, repo],
    queryFn: () =>
      api.get<{ refs: Ref[] }>(
        `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}/tags`,
      ),
  });
  return { branches, tags };
}

/** The ref a view shows: the one chosen, else the repository's default, else
 * whatever exists. A repository whose default branch has no commit yet —
 * which is every repository an agent created and nobody has pushed to —
 * would otherwise show nothing at all. */
export function pickRef(
  refs: Ref[],
  tags: Ref[],
  chosen: string | null,
  defaultBranch: string,
): string {
  if (chosen && [...refs, ...tags].some((r) => r.name === chosen))
    return chosen;
  if (refs.some((r) => r.name === defaultBranch)) return defaultBranch;
  return refs[0]?.name ?? defaultBranch;
}

/** timeAgo renders one of the platform's own timestamps as an age. It is
 * local to the repo screens rather than shared, because Home's copy answers
 * to the desk's needs and these screens need the same question answered in
 * their own tables. */
export function timeAgo(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  if (Number.isNaN(ms)) return "";
  const m = Math.floor(ms / 60_000);
  if (m < 1) return "now";
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  if (d < 60) return `${d}d ago`;
  return `${Math.floor(d / 30)}mo ago`;
}

/** formatBytes reports the size the platform recorded, never a rounded guess
 * at one it does not have. */
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

/** The first line of a commit message is what every list shows; the rest is
 * body and belongs to the detail view only. */
export function commitSubject(message: string): string {
  return message.split("\n")[0] ?? "";
}

/** NotAvailableInline is the platform's honest refusal for a thing the
 * design draws but this deployment has no endpoint for. It is deliberately
 * distinguished from Failed: nothing went wrong, and saying so would make
 * every deployment look broken. */
export function NotAvailableInline({ what }: { what: string }) {
  return <Empty>{what} — not available in this deployment.</Empty>;
}

// ---- Shared control styles. The repo screens were one file once; these keep
// the pieces that split out of it drawing identically.

export const btn: CSSProperties = {
  padding: "5px 11px",
  borderRadius: 6,
  border: "1px solid var(--line)",
  background: "transparent",
  color: "var(--fg)",
  font: "500 12px var(--sans)",
  cursor: "pointer",
};

export const btnPrimary: CSSProperties = {
  ...btn,
  background: "var(--accent-soft)",
  borderColor: "var(--accent)",
  color: "var(--fg)",
};

export const btnDanger: CSSProperties = {
  padding: "7px 14px",
  background: "transparent",
  border: "1px solid #e5534b66",
  borderRadius: 8,
  color: "var(--bad)",
  font: "600 12px var(--sans)",
  cursor: "pointer",
};

export const btnSmall: CSSProperties = {
  background: "transparent",
  border: "1px solid var(--line-2)",
  borderRadius: 6,
  color: "var(--fg-muted)",
  font: "10px var(--sans)",
  padding: "3px 7px",
  cursor: "pointer",
};

export const input: CSSProperties = {
  padding: "5px 9px",
  borderRadius: 6,
  border: "1px solid var(--line)",
  background: "transparent",
  color: "var(--fg)",
  font: "12px var(--sans)",
};

export const selectStyle: CSSProperties = {
  background: "var(--bg)",
  border: "1px solid var(--line-2)",
  borderRadius: 6,
  color: "var(--fg-dim)",
  font: "11px var(--mono)",
  padding: "3px 6px",
  outline: "none",
};

/** row is one labelled settings row: label, control, hint. */
export const adminRow: CSSProperties = {
  display: "flex",
  alignItems: "center",
  gap: 10,
};

export const adminLabel: CSSProperties = {
  font: "600 11px var(--sans)",
  color: "var(--fg-muted)",
  minWidth: 110,
};

/** Tab strip shared by the repository header and the file view's
 * code/blame/history switcher. */
export function Tabs({
  tabs,
  active,
  onPick,
}: {
  tabs: { key: string; label: string; count?: number }[];
  active: string;
  onPick: (key: string) => void;
}) {
  return (
    <div style={{ display: "flex", gap: 2, flexWrap: "wrap" }}>
      {tabs.map((t) => {
        const on = t.key === active;
        return (
          <button
            key={t.key}
            onClick={() => onPick(t.key)}
            aria-current={on ? "page" : undefined}
            style={{
              padding: "7px 12px",
              background: "transparent",
              border: "none",
              borderBottom: `2px solid ${on ? "var(--accent)" : "transparent"}`,
              color: on ? "var(--fg)" : "var(--fg-muted)",
              font: `500 12px var(--sans)`,
              cursor: "pointer",
              display: "flex",
              alignItems: "center",
              gap: 6,
            }}
          >
            {t.label}
            {typeof t.count === "number" ? (
              <span
                style={{
                  background: "var(--line-2)",
                  color: "var(--fg-dim)",
                  borderRadius: 99,
                  padding: "0 6px",
                  font: "600 10px var(--mono)",
                }}
              >
                {t.count}
              </span>
            ) : null}
          </button>
        );
      })}
    </div>
  );
}

/** The same control as btnSmall for react-router's Link, which renders an
 * anchor and so needs the text-decoration reset. */
export const btnSmallLink: React.CSSProperties = {
  ...btnSmall,
  display: "inline-flex",
  alignItems: "center",
  textDecoration: "none",
};

export const mono: CSSProperties = { font: "12px var(--mono)" };
