import { useState } from "react";
import { useMutation, useQueries, useQueryClient } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { api, enc } from "../../lib/api";
import { Empty, Failed, Page, Panel } from "../../components/ui";
import { Dialog } from "../../components/Dialog";
import type { Commit, Ref } from "../../lib/types";
import {
  btn,
  btnSmallLink,
  input,
  Tabs,
  timeAgo,
  useRefs,
  useRepoScope,
} from "./shared";

export function BranchesTab() {
  const { org, repo, repoRecord } = useRepoScope();
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}`;
  const qc = useQueryClient();
  const [params, setParams] = useSearchParams();
  const { branches, tags } = useRefs(org, repo);
  const refs = branches.data?.refs ?? [];
  const tagList = tags.data?.refs ?? [];
  const tab = params.get("tab") ?? "branches";
  const [search, setSearch] = useState("");
  const [creating, setCreating] = useState(false);
  const defaultBranch = repoRecord?.default_branch ?? "main";

  const createBranch = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post(`${base}/branches`, { name: v.name, from: v.from }),
    onSuccess: () => {
      setCreating(false);
      void qc.invalidateQueries({ queryKey: ["branches", org, repo] });
    },
  });

  const needle = search.trim().toLowerCase();
  const matches = (name: string) =>
    needle === "" || name.toLowerCase().includes(needle);
  const branchRows = refs.filter((r) => matches(r.name));
  const tagRows = tagList.filter((r) => matches(r.name));

  // Last commit per ref is one small request each, and it is the only way to
  // show the "UPDATED" column the design draws without inventing dates. The
  // cap bounds the burst: a repository with a hundred branches would
  // otherwise fire a hundred requests on opening the tab.
  const TIP_LIMIT = 24;
  const tip = useQueriesLastCommit(org, repo, [
    ...branchRows.slice(0, TIP_LIMIT),
    ...tagRows.slice(0, TIP_LIMIT),
  ]);

  return (
    <Page
      title="Branches & tags"
      subtitle={`${refs.length} branches · ${tagList.length} tags · default ${defaultBranch}`}
      actions={
        <button style={btn} onClick={() => setCreating(true)}>
          New branch
        </button>
      }
    >
      {creating ? (
        <Dialog
          title="New branch"
          submitLabel="Create"
          fields={[
            {
              name: "name",
              label: "Name",
              required: true,
              placeholder: "feature/x",
            },
            {
              name: "from",
              label: "From",
              placeholder: defaultBranch,
              help: "Empty starts from the repository's default branch.",
            },
          ]}
          busy={createBranch.isPending}
          error={createBranch.error}
          onSubmit={(v) => createBranch.mutate(v)}
          onClose={() => setCreating(false)}
        />
      ) : null}

      <Panel>
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 8,
            flexWrap: "wrap",
            padding: "10px 14px",
            borderBottom: "1px solid var(--line)",
          }}
        >
          <Tabs
            tabs={[
              { key: "branches", label: "Branches", count: refs.length },
              { key: "tags", label: "Tags", count: tagList.length },
            ]}
            active={tab}
            onPick={(k) => {
              const next = new URLSearchParams(params);
              next.set("tab", k);
              setParams(next);
            }}
          />
          <div style={{ flex: 1 }} />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={tab === "branches" ? "Search branches" : "Search tags"}
            aria-label={tab === "branches" ? "Search branches" : "Search tags"}
            style={{ ...input, width: 200 }}
          />
        </div>

        {branches.error || tags.error ? (
          <Failed error={branches.error || tags.error} />
        ) : tab === "branches" ? (
          <div>
            {branchRows.length === 0 ? (
              <Empty>No branches match.</Empty>
            ) : (
              <>
                <RefHeadRow />
                {branchRows.map((r) => (
                  <RefRow
                    key={r.name}
                    refRow={r}
                    tip={tip.get(r.name)}
                    defaultBranch={defaultBranch}
                    repo={repo}
                  />
                ))}
              </>
            )}
          </div>
        ) : (
          <div>
            {tagRows.length === 0 ? (
              <Empty>No tags match.</Empty>
            ) : (
              <>
                <RefHeadRow />
                {tagRows.map((r) => (
                  <RefRow
                    key={r.name}
                    refRow={r}
                    tip={tip.get(r.name)}
                    defaultBranch={defaultBranch}
                    repo={repo}
                  />
                ))}
              </>
            )}
          </div>
        )}
      </Panel>
    </Page>
  );
}

/** RefRow is one branch or tag: the ref's own sha, when its tip last moved,
 * and ways into the tree, the history and the compare view at it. The
 * behind/ahead counts the design draws would need a comparison per ref; the
 * diff endpoint returns text, not counts, so none is shown. */
function RefRow({
  refRow,
  tip,
  defaultBranch,
  repo,
}: {
  refRow: Ref;
  tip: Commit | undefined;
  defaultBranch: string;
  repo: string;
}) {
  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        gap: 10,
        flexWrap: "wrap",
        padding: "9px 14px",
        borderBottom: "1px solid var(--line)",
      }}
    >
      <span style={{ font: "600 12px var(--mono)", flex: 1, minWidth: 160 }}>
        {refRow.name}
        {refRow.name === defaultBranch ? (
          <span
            style={{
              marginLeft: 8,
              background: "var(--accent-soft)",
              borderRadius: 99,
              padding: "1px 7px",
              font: "600 10px var(--mono)",
            }}
          >
            default
          </span>
        ) : null}
      </span>
      <span
        style={{
          font: "11px var(--mono)",
          color: "var(--fg-faint)",
          width: 90,
        }}
      >
        {tip ? timeAgo(tip.at) : "—"}
      </span>
      <span
        style={{
          font: "11px var(--mono)",
          color: "var(--fg-muted)",
          width: 90,
        }}
      >
        {tip ? firstLine(tip.message) : refRow.sha.slice(0, 8)}
      </span>
      <Link
        to={`/repos/${enc(repo)}/code?ref=${encodeURIComponent(refRow.name)}`}
        style={btnSmallLink}
      >
        code
      </Link>
      <Link
        to={`/repos/${enc(repo)}/commits?ref=${encodeURIComponent(refRow.name)}`}
        style={btnSmallLink}
      >
        history
      </Link>
      {refRow.name !== defaultBranch ? (
        <Link
          to={`/repos/${enc(repo)}/compare?base=${encodeURIComponent(defaultBranch)}&head=${encodeURIComponent(refRow.name)}`}
          style={btnSmallLink}
        >
          compare
        </Link>
      ) : null}
    </div>
  );
}

function RefHeadRow() {
  return (
    <div
      style={{
        display: "flex",
        gap: 10,
        padding: "8px 14px",
        borderBottom: "1px solid var(--line)",
        font: "600 10px var(--sans)",
        letterSpacing: ".08em",
        color: "var(--fg-faint)",
      }}
    >
      <span style={{ flex: 1, minWidth: 160 }}>REF</span>
      <span style={{ width: 90 }}>UPDATED</span>
      <span style={{ width: 90 }}>TIP</span>
    </div>
  );
}

function firstLine(message: string): string {
  return message.split("\n")[0] ?? "";
}

/** useQueriesLastCommit asks for each ref's tip commit — one commit per ref,
 * which is the "UPDATED" column. It runs one query per ref and returns them
 * by name, so a ref the request has not answered yet simply shows "—". */
function useQueriesLastCommit(org: string, repo: string, rows: Ref[]) {
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}`;
  const queries = useQueries({
    queries: rows.map((r) => ({
      queryKey: ["commits", org, repo, r.name, 1],
      queryFn: () =>
        api.get<{ commits: Commit[] }>(
          `${base}/commits/${enc(r.name)}?limit=1`,
        ),
      enabled: rows.length > 0,
      retry: false,
      staleTime: 60_000,
    })),
  });
  const byName = new Map<string, Commit | undefined>();
  rows.forEach((r, i) => {
    byName.set(r.name, queries[i]?.data?.commits[0]);
  });
  return byName;
}
