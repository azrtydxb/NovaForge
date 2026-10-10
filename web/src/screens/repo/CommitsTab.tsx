import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { api, enc } from "../../lib/api";
import { Async, Empty, Failed, Page, Panel } from "../../components/ui";
import type { Commit } from "../../lib/types";
import {
  btnSmall,
  btnSmallLink,
  input,
  pickRef,
  selectStyle,
  timeAgo,
  useRefs,
  useRepoScope,
} from "./shared";

/** CommitsTab is the design's commits screen: a searchable list grouped by
 * day, each commit carrying its full SHA, when it landed, and a way into the
 * tree as it stood at that commit. The platform serves a bounded window of
 * history (50 by default, more on request), so the list says how many it is
 * showing rather than a total it cannot know. */
export function CommitsTab() {
  const { org, repo, repoRecord } = useRepoScope();
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}`;
  const [params, setParams] = useSearchParams();
  const ref = params.get("ref") ?? "";
  const { branches, tags } = useRefs(org, repo);
  const refs = branches.data?.refs ?? [];
  const tagList = tags.data?.refs ?? [];
  const head = pickRef(
    refs,
    tagList,
    ref || null,
    repoRecord?.default_branch ?? "main",
  );

  // A bounded window: the platform serves up to `limit` commits and no
  // total, so the screen says what it is showing instead of a count it
  // cannot know.
  const LIMIT = 200;
  const commits = useQuery({
    queryKey: ["commits", org, repo, head, LIMIT],
    queryFn: () =>
      api.get<{ commits: Commit[] }>(
        `${base}/commits/${enc(head)}?limit=${LIMIT}`,
      ),
    enabled: branches.isSuccess,
  });

  const [search, setSearch] = useState("");
  const [page, setPage] = useState(0);
  const PER_PAGE = 25;

  const needle = search.trim().toLowerCase();
  const filtered = (commits.data?.commits ?? []).filter((c) => {
    if (needle === "") return true;
    return (
      c.message.toLowerCase().includes(needle) ||
      c.sha.toLowerCase().includes(needle) ||
      c.author_name.toLowerCase().includes(needle)
    );
  });
  const pages = Math.max(1, Math.ceil(filtered.length / PER_PAGE));
  const shown = filtered.slice(page * PER_PAGE, (page + 1) * PER_PAGE);

  // Group by the calendar day the platform recorded, newest first — the
  // design's "Commits on <date>" headings.
  const groups: { day: string; commits: Commit[] }[] = [];
  for (const c of shown) {
    const day = new Date(c.at).toDateString();
    const last = groups[groups.length - 1];
    if (last && last.day === day) last.commits.push(c);
    else groups.push({ day, commits: [c] });
  }

  return (
    <Page title="Commits" subtitle={`Most recent ${LIMIT} commits on ${head}`}>
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
          <select
            aria-label="Branch or tag"
            value={head}
            onChange={(e) => {
              const next = new URLSearchParams(params);
              next.set("ref", e.target.value);
              setParams(next);
              setPage(0);
            }}
            style={{ ...selectStyle, maxWidth: 240 }}
          >
            <optgroup label="Branches">
              {refs.map((r) => (
                <option key={r.name} value={r.name}>
                  {r.name}
                </option>
              ))}
            </optgroup>
            {tagList.length > 0 ? (
              <optgroup label="Tags">
                {tagList.map((r) => (
                  <option key={`tag:${r.name}`} value={r.name}>
                    {r.name}
                  </option>
                ))}
              </optgroup>
            ) : null}
          </select>
          <input
            value={search}
            onChange={(e) => {
              setSearch(e.target.value);
              setPage(0);
            }}
            placeholder="Search messages or SHAs"
            aria-label="Search commits"
            style={{ ...input, flex: 1, minWidth: 200 }}
          />
          <span style={{ font: "11px var(--mono)", color: "var(--fg-faint)" }}>
            {filtered.length} shown
          </span>
        </div>

        {branches.error ? (
          <Failed error={branches.error} />
        ) : branches.isSuccess && refs.length === 0 ? (
          <Empty>This repository has no branches yet.</Empty>
        ) : (
          <Async query={commits}>
            {(d) =>
              d.commits.length === 0 ? (
                <Empty>No commits on {head}.</Empty>
              ) : (
                <div>
                  {groups.map((g) => (
                    <div key={g.day}>
                      <div
                        style={{
                          padding: "8px 14px",
                          borderBottom: "1px solid var(--line)",
                          font: "600 11px var(--sans)",
                          letterSpacing: ".08em",
                          color: "var(--fg-muted)",
                          background: "var(--panel-2)",
                        }}
                      >
                        Commits on {g.day}
                      </div>
                      {g.commits.map((c) => (
                        <div
                          key={c.sha}
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: 10,
                            flexWrap: "wrap",
                            padding: "9px 14px",
                            borderBottom: "1px solid var(--line)",
                          }}
                        >
                          <span
                            style={{ font: "600 11px var(--mono)", width: 30 }}
                          >
                            {initials(c.author_name)}
                          </span>
                          <span
                            style={{
                              font: "12px var(--sans)",
                              flex: 1,
                              minWidth: 200,
                            }}
                          >
                            {firstLine(c.message)}
                          </span>
                          <span
                            style={{
                              font: "11px var(--mono)",
                              color: "var(--fg-faint)",
                            }}
                          >
                            {c.author_name} · {timeAgo(c.at)}
                          </span>
                          <span
                            style={{
                              font: "11px var(--mono)",
                              color: "var(--fg-muted)",
                            }}
                          >
                            {c.sha.slice(0, 8)}
                          </span>
                          <button
                            style={btnSmall}
                            onClick={() =>
                              void navigator.clipboard.writeText(c.sha)
                            }
                          >
                            copy SHA
                          </button>
                          <Link
                            to={`/repos/${enc(repo)}/code?ref=${encodeURIComponent(c.sha)}`}
                            style={btnSmallLink}
                          >
                            browse files
                          </Link>
                        </div>
                      ))}
                    </div>
                  ))}
                </div>
              )
            }
          </Async>
        )}
      </Panel>

      {pages > 1 ? (
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 8,
            marginTop: 12,
          }}
        >
          <button
            style={btnSmall}
            disabled={page === 0}
            onClick={() => setPage(page - 1)}
          >
            Previous
          </button>
          <span style={{ font: "11px var(--mono)", color: "var(--fg-muted)" }}>
            Page {page + 1} of {pages}
          </span>
          <button
            style={btnSmall}
            disabled={page >= pages - 1}
            onClick={() => setPage(page + 1)}
          >
            Next
          </button>
        </div>
      ) : null}
    </Page>
  );
}

function firstLine(message: string): string {
  return message.split("\n")[0] ?? "";
}

function initials(name: string): string {
  return name
    .split(/\s+/)
    .map((p) => p[0] ?? "")
    .join("")
    .slice(0, 2)
    .toUpperCase();
}
