import { useMemo, useState } from "react";
import { useMutation, useQueries } from "@tanstack/react-query";
import { Link, Route, Routes } from "react-router-dom";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import {
  Empty,
  Failed,
  Loading,
  Page,
  Panel,
  PanelHead,
  StatePill,
} from "../components/ui";
import { Dialog } from "../components/Dialog";
import { RepoPage } from "./repo/RepoPage";
import { btn, btnPrimary, input, selectStyle, timeAgo } from "./repo/shared";
import type { CIRun, Commit, Mirror, Repo } from "../lib/types";

/** Repos is the /repos route: the repositories index at the root, and the
 * repository subtree (code, commits, branches, compare, releases, settings)
 * under /repos/:repo, which RepoPage owns. */
export function Repos() {
  return (
    <Routes>
      <Route index element={<RepoIndex />} />
      <Route path=":repo/*" element={<RepoPage />} />
      <Route path=":repo" element={<RepoPage />} />
      <Route path="*" element={<RepoIndex />} />
    </Routes>
  );
}

type Filter = "All" | "Sources" | "Forks" | "Mirrors" | "Archived";

const FILTERS: Filter[] = ["All", "Sources", "Forks", "Mirrors", "Archived"];

/** RepoIndex is the design's repositories index: one card per repository
 * with what the platform knows about it, a find box, the type filters, a
 * sort, and the organization's mirror summary. Every number on a card comes
 * from an endpoint; the design's stars, open work and agent counts have no
 * endpoints here and are not faked. */
function RepoIndex() {
  const w = useWorkspace();
  const org = w.org ?? "";
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState<Filter>("All");
  const [sort, setSort] = useState<"updated" | "name">("updated");
  const [page, setPage] = useState(0);
  const [creating, setCreating] = useState(false);
  const [importing, setImporting] = useState(false);

  const repos = w.repos;
  const needle = search.trim().toLowerCase();

  // Mirror state decides both the Mirrors filter and the per-card mirror
  // badge. There is no list endpoint, so each repository is asked; a 404 is
  // the answer "not a mirror", not a failure, which is why none of these
  // retry.
  const mirrors = useQueriesMirrorState(org, repos);

  // The "Updated" column and the "last updated" sort both need each
  // repository's tip commit; one commit per repository is asked for.
  const tips = useQueriesTip(org, repos);

  const filtered = repos.filter((r) => {
    if (needle !== "" && !r.name.toLowerCase().includes(needle)) return false;
    if (filter === "Sources") return r.parent_repo_id === "";
    if (filter === "Forks") return r.parent_repo_id !== "";
    if (filter === "Archived") return r.archived;
    if (filter === "Mirrors") return mirrors.get(r.name)?.remote !== undefined;
    return true;
  });

  const sorted = useMemo(() => {
    const byName = [...filtered].sort((a, b) => a.name.localeCompare(b.name));
    if (sort === "name") return byName;
    // A repository with no commits yet has no "updated" to sort by, so it
    // sorts after every one that has.
    return byName.sort((a, b) => {
      const ta = tips.get(a.name)?.at ?? "";
      const tb = tips.get(b.name)?.at ?? "";
      if (ta === "" && tb === "") return 0;
      if (ta === "") return 1;
      if (tb === "") return -1;
      return ta < tb ? 1 : -1;
    });
  }, [filtered, sort, tips]);

  const PER_PAGE = 8;
  const pages = Math.max(1, Math.ceil(sorted.length / PER_PAGE));
  const pageRows = sorted.slice(page * PER_PAGE, (page + 1) * PER_PAGE);
  // CI state is fetched for the visible page only, so an organization with
  // many repositories does not fire a request per repository on every load.
  const ci = useQueriesCI(org, pageRows);

  if (w.loading) return <Loading />;
  if (w.error)
    return (
      <Panel>
        <Failed error={w.error} />
      </Panel>
    );

  const mirrored = repos.filter(
    (r) => mirrors.get(r.name)?.remote !== undefined,
  );

  return (
    <Page
      title="Repositories"
      subtitle={
        repos.length === 0
          ? "This organization has no repositories yet."
          : `${repos.length} repositories · ${repos.filter((r) => r.parent_repo_id !== "").length} forks · ${repos.filter((r) => r.archived).length} archived`
      }
      actions={
        <div style={{ display: "flex", gap: 6 }}>
          <button style={btn} onClick={() => setCreating(true)}>
            New repository
          </button>
          <button style={btnPrimary} onClick={() => setImporting(true)}>
            Import repository
          </button>
        </div>
      }
    >
      {creating && org !== "" ? (
        <NewRepoDialog
          org={org}
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            void w.retry();
          }}
        />
      ) : null}
      {importing && org !== "" ? (
        <ImportDialog org={org} onClose={() => setImporting(false)} />
      ) : null}

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "minmax(0,1fr) 260px",
          gap: 14,
          alignItems: "start",
        }}
      >
        <div style={{ minWidth: 0 }}>
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: 10,
              flexWrap: "wrap",
              marginBottom: 10,
            }}
          >
            <input
              value={search}
              onChange={(e) => {
                setSearch(e.target.value);
                setPage(0);
              }}
              placeholder="Find a repository…"
              aria-label="Find a repository"
              style={{ ...input, flex: 1, minWidth: 180 }}
            />
            <div
              role="radiogroup"
              aria-label="Filter repositories"
              style={{ display: "flex", gap: 2 }}
            >
              {FILTERS.map((f) => (
                <button
                  key={f}
                  role="radio"
                  aria-checked={filter === f}
                  onClick={() => {
                    setFilter(f);
                    setPage(0);
                  }}
                  style={{
                    padding: "5px 10px",
                    borderRadius: 7,
                    border: "1px solid var(--line)",
                    background:
                      filter === f ? "var(--accent-soft)" : "transparent",
                    color: filter === f ? "var(--fg)" : "var(--fg-muted)",
                    font: "500 11px var(--sans)",
                    cursor: "pointer",
                  }}
                >
                  {f}
                </button>
              ))}
            </div>
            <select
              aria-label="Sort repositories"
              value={sort}
              onChange={(e) => setSort(e.target.value as "updated" | "name")}
              style={{ ...selectStyle, maxWidth: 150 }}
            >
              <option value="updated">Last updated</option>
              <option value="name">Name</option>
            </select>
          </div>

          <Panel>
            <PanelHead title="REPOSITORY LIST" count={sorted.length}>
              <div style={{ flex: 1 }} />
              <span
                style={{ font: "10px var(--sans)", color: "var(--fg-faint)" }}
              >
                {sorted.length} of {repos.length} repositories ·{" "}
                {sort === "updated" ? "last updated" : "by name"}
              </span>
            </PanelHead>
            {repos.length === 0 ? (
              <Empty>
                No repositories yet. Create one, or import one from another Git
                host.
              </Empty>
            ) : pageRows.length === 0 ? (
              <Empty>Nothing matches this filter.</Empty>
            ) : (
              pageRows.map((r) => (
                <RepoCard
                  key={r.id}
                  repo={r}
                  repos={repos}
                  tip={tips.get(r.name)}
                  mirror={mirrors.get(r.name)}
                  ci={ci.get(r.name)}
                />
              ))
            )}
            {pages > 1 ? (
              <div
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: 8,
                  padding: "9px 14px",
                }}
              >
                <button
                  style={btn}
                  disabled={page === 0}
                  onClick={() => setPage(page - 1)}
                >
                  Previous
                </button>
                <span
                  style={{ font: "11px var(--mono)", color: "var(--fg-muted)" }}
                >
                  {page * PER_PAGE + 1}–
                  {Math.min(sorted.length, (page + 1) * PER_PAGE)} of{" "}
                  {sorted.length}
                </span>
                <button
                  style={btn}
                  disabled={page >= pages - 1}
                  onClick={() => setPage(page + 1)}
                >
                  Next
                </button>
              </div>
            ) : null}
          </Panel>
        </div>

        <div style={{ display: "grid", gap: 14 }}>
          <Panel>
            <PanelHead title="MIRRORS" />
            <div style={{ padding: 12, display: "grid", gap: 8 }}>
              {mirrored.length === 0 ? (
                <span
                  style={{ font: "12px var(--sans)", color: "var(--fg-muted)" }}
                >
                  Nothing in this organization follows an upstream.
                </span>
              ) : (
                mirrored.map((r) => {
                  const m = mirrors.get(r.name);
                  return (
                    <div key={r.id} style={{ display: "grid", gap: 2 }}>
                      <Link
                        to={`/repos/${enc(r.name)}/settings`}
                        style={{ font: "600 12px var(--mono)" }}
                      >
                        {r.name}
                      </Link>
                      <span
                        style={{
                          font: "11px var(--sans)",
                          color:
                            m && m.last_error !== ""
                              ? "var(--bad)"
                              : "var(--fg-muted)",
                        }}
                      >
                        {m === undefined
                          ? "checking…"
                          : m.last_error !== ""
                            ? `failed ${timeAgo(m.last_synced_at)} · ${m.last_error}`
                            : `synced ${timeAgo(m.last_synced_at)}`}
                      </span>
                    </div>
                  );
                })
              )}
            </div>
          </Panel>
        </div>
      </div>
    </Page>
  );
}

/** RepoCard is one repository in the index. Its badges are the platform's
 * own records: archived from the repository, a fork from its parent link,
 * a mirror from the mirror endpoint's answer. The design's stars, open work
 * and agent counts have no endpoints behind them, so the card shows none. */
function RepoCard({
  repo,
  repos,
  tip,
  mirror,
  ci,
}: {
  repo: Repo;
  repos: Repo[];
  tip: Commit | undefined;
  mirror: Mirror | undefined;
  ci: CIRun | undefined;
}) {
  const parent = repo.parent_repo_id
    ? repos.find((r) => r.id === repo.parent_repo_id)
    : undefined;
  return (
    <div
      style={{
        padding: "12px 14px",
        borderBottom: "1px solid var(--line)",
        display: "grid",
        gap: 6,
      }}
    >
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 10,
          flexWrap: "wrap",
        }}
      >
        <Link
          to={`/repos/${enc(repo.name)}`}
          style={{ font: "600 14px var(--sans)" }}
        >
          {repo.name}
        </Link>
        {repo.archived ? (
          <span
            style={{
              background: "var(--warn-bg)",
              color: "var(--warn)",
              borderRadius: 99,
              padding: "1px 8px",
              font: "600 10px var(--mono)",
            }}
          >
            archived
          </span>
        ) : null}
        {repo.parent_repo_id !== "" ? (
          <span
            title={
              parent
                ? `forked from ${parent.name}`
                : "forked from a repository this organization can no longer see"
            }
            style={{
              background: "var(--accent-soft)",
              color: "var(--fg-muted)",
              borderRadius: 99,
              padding: "1px 8px",
              font: "600 10px var(--mono)",
            }}
          >
            fork{parent ? ` of ${parent.name}` : ""}
          </span>
        ) : null}
        {mirror?.remote !== undefined ? (
          <span
            title={`read-only mirror of ${mirror.remote}, refreshed every ${
              mirror.interval_seconds === 0
                ? "mirrorer pass"
                : `${mirror.interval_seconds}s`
            }`}
            style={{
              background: "var(--line-2)",
              color: "var(--fg-muted)",
              borderRadius: 99,
              padding: "1px 8px",
              font: "600 10px var(--mono)",
            }}
          >
            mirror
          </span>
        ) : null}
        <div style={{ flex: 1 }} />
        <span style={{ font: "11px var(--mono)", color: "var(--fg-faint)" }}>
          {tip ? `updated ${timeAgo(tip.at)}` : "no commits yet"}
        </span>
        {ci ? <StatePill state={ci.status} /> : null}
      </div>
      <div style={{ display: "flex", gap: 12, flexWrap: "wrap" }}>
        <span style={{ font: "11px var(--mono)", color: "var(--fg-muted)" }}>
          default {repo.default_branch}
        </span>
        {tip ? (
          <span
            style={{
              font: "11px var(--mono)",
              color: "var(--fg-faint)",
              overflow: "hidden",
              textOverflow: "ellipsis",
              whiteSpace: "nowrap",
              maxWidth: 420,
            }}
          >
            {tip.sha.slice(0, 8)} {tip.message.split("\n")[0]}
          </span>
        ) : null}
      </div>
    </div>
  );
}

/** NewRepoDialog creates an empty repository. The platform creates it with
 * no commits; the tree view of an empty repository says what to do next. */
function NewRepoDialog({
  org,
  onClose,
  onCreated,
}: {
  org: string;
  onClose: () => void;
  onCreated: () => void;
}) {
  const create = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post<Repo>(`/api/v1/orgs/${enc(org)}/repos`, { name: v.name }),
    onSuccess: onCreated,
  });
  return (
    <Dialog
      title="New repository"
      description="Creates an empty repository in this organization. Push to it, or import one from another Git host to bring history across."
      submitLabel="Create"
      busy={create.isPending}
      error={create.error}
      fields={[
        { name: "name", label: "Name", required: true, placeholder: "project" },
      ]}
      onSubmit={(v) => create.mutate(v)}
      onClose={onClose}
    />
  );
}

const ONE_OFF = "one-off copy";
const MIRRORED = "keep mirrored";

/** ImportDialog brings a repository in from another Git host.
 *
 * The token field is a password field and the help text says where the value
 * goes, because the honest alternative — pasting a token into the URL, which
 * is what every Git host's own instructions do — is refused by the platform
 * on purpose: a URL with a token in it ends up in tables and log lines. */
function ImportDialog({ org, onClose }: { org: string; onClose: () => void }) {
  const w = useWorkspace();
  const imported = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post<{ repo: Repo; mirror: Mirror | null }>(
        `/api/v1/orgs/${enc(org)}/repos/import`,
        {
          name: v.name,
          remote: v.remote,
          credential: v.credential,
          mirror: v.mode === MIRRORED,
          interval_seconds: Number(v.interval) || 0,
        },
      ),
    onSuccess: () => {
      onClose();
      void w.retry();
    },
  });

  return (
    <Dialog
      title="Import a repository"
      description="Imports Git refs and history from an operator-approved host. LFS payloads, release files, users, issues and CI records need separate migration. Nothing is created unless the clone finishes; failed imports can be retried."
      submitLabel="Import"
      busy={imported.isPending}
      error={imported.error}
      fields={[
        {
          name: "remote",
          label: "Upstream URL",
          placeholder: "https://github.com/owner/project.git",
          required: true,
          help: "http or https only. A URL containing a username or token is refused — use the token field instead.",
        },
        {
          name: "name",
          label: "Name here",
          placeholder: "project",
          required: true,
        },
        {
          name: "credential",
          label: "Access token",
          type: "password",
          help: "Only needed for a private upstream. It is stored encrypted and never shown again.",
        },
        {
          name: "mode",
          label: "Afterwards",
          type: "select",
          options: [ONE_OFF, MIRRORED],
          help: `${ONE_OFF} leaves the repository writable here. ${MIRRORED} keeps following upstream, and refuses pushes.`,
        },
        {
          name: "interval",
          label: "Refresh every",
          placeholder: "3600 seconds",
          help: "Only used when mirrored; blank means hourly.",
        },
      ]}
      onSubmit={(values) => imported.mutate(values)}
      onClose={onClose}
    />
  );
}

/** useQueriesMirrorState asks each repository whether it follows an
 * upstream. A 404 is the answer "not a mirror" and is not retried; the map
 * holds a Mirror only for the repositories that are. */
function useQueriesMirrorState(org: string, repos: Repo[]) {
  const queries = useQueries({
    queries: repos.map((r) => ({
      queryKey: ["mirror", org, r.name],
      queryFn: () =>
        api.get<Mirror>(`/api/v1/orgs/${enc(org)}/repos/${enc(r.name)}/mirror`),
      enabled: org !== "" && repos.length > 0,
      retry: false,
      staleTime: 60_000,
    })),
  });
  const byName = new Map<string, Mirror>();
  repos.forEach((r, i) => {
    const m = queries[i]?.data;
    if (m) byName.set(r.name, m);
  });
  return byName;
}

/** useQueriesTip fetches each repository's tip commit — one commit each —
 * for the "updated" column and the last-updated sort. */
function useQueriesTip(org: string, repos: Repo[]) {
  const queries = useQueries({
    queries: repos.map((r) => ({
      queryKey: ["commits", org, r.name, r.default_branch, 1],
      queryFn: () =>
        api.get<{ commits: Commit[] }>(
          `/api/v1/orgs/${enc(org)}/repos/${enc(r.name)}/commits/${enc(r.default_branch)}?limit=1`,
        ),
      enabled: org !== "" && repos.length > 0,
      retry: false,
      staleTime: 60_000,
    })),
  });
  const byName = new Map<string, Commit | undefined>();
  repos.forEach((r, i) => {
    byName.set(r.name, queries[i]?.data?.commits[0]);
  });
  return byName;
}

/** useQueriesCI fetches the visible page's latest CI run on each
 * repository's default branch, newest first as the platform serves them. */
function useQueriesCI(org: string, repos: Repo[]) {
  const queries = useQueries({
    queries: repos.map((r) => ({
      queryKey: ["ci", org, r.name],
      queryFn: () =>
        api.get<{ runs: CIRun[] }>(
          `/api/v1/orgs/${enc(org)}/repos/${enc(r.name)}/ci/runs`,
        ),
      enabled: org !== "" && repos.length > 0,
      retry: false,
      staleTime: 30_000,
    })),
  });
  const byName = new Map<string, CIRun | undefined>();
  repos.forEach((r, i) => {
    const onDefault = (queries[i]?.data?.runs ?? [])
      .filter((run) => run.ref === r.default_branch)
      .sort((a, b) => (a.created_at < b.created_at ? 1 : -1))[0];
    byName.set(r.name, onDefault);
  });
  return byName;
}
