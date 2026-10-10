import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { api, enc } from "../../lib/api";
import { Async, Empty, Failed, Panel, PanelHead } from "../../components/ui";
import type { Commit, TreeEntry } from "../../lib/types";
import { RepoDetails } from "./RepoDetails";
import {
  btnSmall,
  commitSubject,
  formatBytes,
  input,
  NotAvailableInline,
  pickRef,
  selectStyle,
  timeAgo,
  useRefs,
  useRepoScope,
} from "./shared";

function encodePath(p: string): string {
  return p.split("/").filter(Boolean).map(enc).join("/");
}

function pathSegments(p: string): string[] {
  return p.split("/").filter(Boolean);
}

/** CodeTab is the repository's landing tab: the ref picker, the tree browsed
 * one directory at a time, the ref's tip commit, the README, and — once a
 * file is opened — the file view with its code/blame/history switch. The
 * selected ref, directory and file travel in the query string so a directory
 * or a file can be linked, and because branch names carry slashes ("agents/
 * NF-1/work"), which a path segment would not survive unencoded. */
export function CodeTab() {
  const { org, repo, repoRecord } = useRepoScope();
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}`;
  const [params, setParams] = useSearchParams();
  const ref = params.get("ref");
  const path = params.get("path") ?? "";
  const file = params.get("file");
  const view = params.get("view") ?? "code";

  const setParam = (patch: Record<string, string | null>) => {
    const next = new URLSearchParams(params);
    // A file belongs to the path it was opened under: changing the ref or the
    // directory closes it, because the same name at the new ref is a
    // different file.
    if ("ref" in patch || "path" in patch) {
      next.delete("file");
      next.delete("view");
    }
    for (const [k, v] of Object.entries(patch)) {
      if (v === null) next.delete(k);
      else next.set(k, v);
    }
    setParams(next);
  };

  const { branches, tags } = useRefs(org, repo);
  const refs = branches.data?.refs ?? [];
  const tagList = tags.data?.refs ?? [];
  const defaultBranch = repoRecord?.default_branch ?? "main";
  const head = pickRef(refs, tagList, ref, defaultBranch);

  // A repository with no branch has no commit to read a tree from. Asking
  // anyway answered 404, which the old panel showed as "not available in
  // this deployment" — wrong on both counts for an empty repository.
  const emptyRepo = branches.data !== undefined && refs.length === 0;

  const tree = useQuery({
    queryKey: ["tree", org, repo, head, path],
    queryFn: () =>
      api.get<{ entries: TreeEntry[] }>(
        `${base}/tree/${enc(head)}/${encodePath(path)}`,
      ),
    enabled: branches.isSuccess && !emptyRepo,
  });

  // A blob is served as raw bytes, not JSON: a file is bytes, and wrapping it
  // in JSON would mean base64 and a size limit. The fetch happens only once a
  // file is actually open.
  const blob = useQuery({
    queryKey: ["blob", org, repo, head, file],
    queryFn: () =>
      api.text(`${base}/blob/${enc(head)}/${encodePath(file ?? "")}`),
    enabled: Boolean(file),
  });

  // The ref's tip commit, for the bar above the tree. One commit is asked
  // for because that is all the bar shows; the total count on the ref is not
  // something the platform serves, so none is shown.
  const tip = useQuery({
    queryKey: ["commits", org, repo, head, 1],
    queryFn: () =>
      api.get<{ commits: Commit[] }>(`${base}/commits/${enc(head)}?limit=1`),
    enabled: branches.isSuccess && !emptyRepo,
  });

  const [filter, setFilter] = useState("");
  const entries = (tree.data?.entries ?? []).filter((e) =>
    filter === "" ? true : e.name.toLowerCase().includes(filter.toLowerCase()),
  );
  const sorted = [...entries].sort((a, b) => {
    if (a.kind !== b.kind) return a.kind === "tree" ? -1 : 1;
    return a.name.localeCompare(b.name);
  });

  const readmeName = (tree.data?.entries ?? []).find(
    (e) => e.kind === "blob" && e.name.toLowerCase() === "readme.md",
  )?.name;

  return (
    <div
      style={{
        display: "grid",
        gridTemplateColumns: "minmax(0,1fr) 300px",
        gap: 14,
        alignItems: "start",
      }}
    >
      <div style={{ display: "grid", gap: 14, minWidth: 0 }}>
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
              onChange={(e) => setParam({ ref: e.target.value, path: null })}
              style={{ ...selectStyle, maxWidth: 240 }}
            >
              {refs.length === 0 ? <option>{head}</option> : null}
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
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Go to file…"
              aria-label="Go to file"
              style={{ ...input, width: 180, font: "11px var(--mono)" }}
            />
            <div style={{ flex: 1 }} />
            <span
              style={{ font: "11px var(--mono)", color: "var(--fg-faint)" }}
            >
              {path === "" ? repo : path}
            </span>
          </div>

          <div
            style={{
              padding: "8px 14px",
              borderBottom: "1px solid var(--line)",
              font: "11px var(--mono)",
              color: "var(--fg-muted)",
              display: "flex",
              gap: 2,
              flexWrap: "wrap",
              alignItems: "center",
            }}
          >
            <button style={crumb} onClick={() => setParam({ path: null })}>
              {repo}
            </button>
            {pathSegments(path).map((s, i) => (
              <button
                key={`${s}:${i}`}
                style={crumb}
                onClick={() =>
                  setParam({
                    path: pathSegments(path)
                      .slice(0, i + 1)
                      .join("/"),
                  })
                }
              >
                {s}/
              </button>
            ))}
          </div>

          {branches.isSuccess && !emptyRepo && tip.data?.commits[0] ? (
            <div
              style={{
                display: "flex",
                alignItems: "center",
                gap: 10,
                flexWrap: "wrap",
                padding: "8px 14px",
                borderBottom: "1px solid var(--line)",
              }}
            >
              <span
                style={{
                  background: "var(--accent-soft)",
                  borderRadius: 6,
                  padding: "1px 7px",
                  font: "600 11px var(--mono)",
                }}
              >
                {tip.data.commits[0].sha.slice(0, 8)}
              </span>
              <span style={{ font: "12px var(--sans)" }}>
                {commitSubject(tip.data.commits[0].message)}
              </span>
              <span
                style={{ font: "11px var(--mono)", color: "var(--fg-faint)" }}
              >
                {tip.data.commits[0].author_name} ·{" "}
                {timeAgo(tip.data.commits[0].at)}
              </span>
              <div style={{ flex: 1 }} />
              <Link
                to={`/repos/${enc(repo)}/commits`}
                style={{ font: "11px var(--mono)", flexShrink: 0 }}
              >
                history
              </Link>
            </div>
          ) : null}

          {branches.error || tags.error ? (
            <Failed error={branches.error || tags.error} />
          ) : emptyRepo ? (
            <Empty>
              This repository is empty. Push a first commit to {defaultBranch}.
            </Empty>
          ) : (
            <Async query={tree}>
              {(d) =>
                d.entries.length === 0 ? (
                  <Empty>This directory is empty.</Empty>
                ) : (
                  <div>
                    {path !== ""
                      ? buttonUp({
                          setParam,
                          parent: pathSegments(path).slice(0, -1).join("/"),
                        })
                      : null}
                    {sorted.map((e) => (
                      <button
                        key={e.name}
                        style={{
                          display: "flex",
                          alignItems: "center",
                          gap: 8,
                          width: "100%",
                          textAlign: "left",
                          padding: "6px 14px",
                          background: "transparent",
                          border: "none",
                          borderBottom: "1px solid var(--line)",
                          color:
                            e.kind === "tree" ? "var(--fg-dim)" : "var(--link)",
                          font: "12px var(--mono)",
                          cursor: "pointer",
                        }}
                        onClick={() => {
                          const next = path ? `${path}/${e.name}` : e.name;
                          if (e.kind === "tree") setParam({ path: next });
                          else setParam({ file: next });
                        }}
                      >
                        <span style={{ width: 14 }}>
                          {e.kind === "tree" ? "▸" : ""}
                        </span>
                        <span style={{ flex: 1 }}>{e.name}</span>
                        {e.kind === "blob" ? (
                          <span
                            style={{
                              font: "11px var(--mono)",
                              color: "var(--fg-faint)",
                            }}
                          >
                            {formatBytes(e.size)}
                          </span>
                        ) : null}
                      </button>
                    ))}
                  </div>
                )
              }
            </Async>
          )}
        </Panel>

        {file !== null && file !== "" ? (
          <FileView
            base={base}
            refName={head}
            file={file}
            view={view}
            setParam={setParam}
            content={blob}
          />
        ) : null}

        {readmeName !== undefined && (file === null || file === "") ? (
          <ReadmePanel
            base={base}
            org={org}
            repo={repo}
            refName={head}
            path={path}
            name={readmeName}
          />
        ) : null}
      </div>

      <RepoDetails />
    </div>
  );
}

function buttonUp({
  setParam,
  parent,
}: {
  setParam: (patch: Record<string, string | null>) => void;
  parent: string;
}) {
  return (
    <button
      style={{
        display: "block",
        width: "100%",
        textAlign: "left",
        padding: "6px 14px",
        background: "transparent",
        border: "none",
        borderBottom: "1px solid var(--line)",
        color: "var(--fg-muted)",
        font: "12px var(--mono)",
        cursor: "pointer",
        boxSizing: "border-box",
      }}
      onClick={() => setParam({ path: parent === "" ? null : parent })}
    >
      ../
    </button>
  );
}

/** FileView is one file at one ref, with the design's code/blame/history
 * switch. Blame and per-file history have no endpoints in this deployment,
 * so those tabs render the platform's refusal rather than a guess. */
function FileView({
  base,
  refName,
  file,
  view,
  setParam,
  content,
}: {
  base: string;
  refName: string;
  file: string;
  view: string;
  setParam: (patch: Record<string, string | null>) => void;
  content: { isLoading: boolean; error: unknown; data: string | undefined };
}) {
  const fileBase = file.split("/").pop() ?? file;
  const lines = (content.data ?? "").split("\n");
  // A trailing newline makes a final "" element, which would render as a
  // phantom last line number. Drop exactly that one.
  if (lines.length > 1 && lines[lines.length - 1] === "") lines.pop();

  return (
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
        <span style={{ font: "600 12px var(--mono)" }}>{fileBase}</span>
        <span style={{ font: "11px var(--mono)", color: "var(--fg-faint)" }}>
          {lines.length} lines
          {content.data !== undefined
            ? ` · ${formatBytes(new Blob([content.data]).size)}`
            : ""}
          {" · "}
          {refName}
        </span>
        <div style={{ flex: 1 }} />
        <button
          style={btnSmall}
          disabled={content.data === undefined}
          onClick={() => void navigator.clipboard.writeText(content.data ?? "")}
        >
          copy contents
        </button>
        <button
          style={btnSmall}
          onClick={() =>
            void api.download(
              `${base}/blob/${enc(refName)}/${encodePath(file)}`,
              fileBase,
            )
          }
        >
          download
        </button>
      </div>
      <div
        style={{
          display: "flex",
          gap: 2,
          padding: "0 14px",
          borderBottom: "1px solid var(--line)",
        }}
      >
        {(
          [
            ["code", "Code"],
            ["blame", "Blame"],
            ["history", "History"],
          ] as const
        ).map(([k, label]) => (
          <button
            key={k}
            aria-current={view === k ? "page" : undefined}
            onClick={() => setParam({ view: k })}
            style={{
              padding: "7px 10px",
              background: "transparent",
              border: "none",
              borderBottom: `2px solid ${view === k ? "var(--accent)" : "transparent"}`,
              color: view === k ? "var(--fg)" : "var(--fg-muted)",
              font: "500 12px var(--sans)",
              cursor: "pointer",
            }}
          >
            {label}
          </button>
        ))}
      </div>
      {view === "blame" ? (
        <NotAvailableInline what="Blame" />
      ) : view === "history" ? (
        <NotAvailableInline what="Per-file history" />
      ) : (
        <Async query={content}>
          {(text) => (
            <pre
              style={{
                margin: 0,
                padding: 16,
                overflow: "auto",
                maxHeight: "calc(100vh - 320px)",
                font: "12px/1.65 var(--mono)",
                color: "var(--fg-dim)",
                whiteSpace: "pre",
                tabSize: 8,
              }}
            >
              {text.split("\n").map((line, i) => (
                <div key={i} style={{ display: "flex" }}>
                  <span
                    style={{
                      width: 44,
                      flexShrink: 0,
                      textAlign: "right",
                      paddingRight: 14,
                      color: "var(--fg-faint)",
                      userSelect: "none",
                    }}
                  >
                    {i + 1}
                  </span>
                  <span
                    style={{ whiteSpace: "pre-wrap", wordBreak: "break-word" }}
                  >
                    {line}
                  </span>
                </div>
              ))}
            </pre>
          )}
        </Async>
      )}
    </Panel>
  );
}

/** ReadmePanel shows the README of the directory being browsed, fetched as
 * the same blob any other file is. There is no markdown renderer in this
 * application, so the source is shown as the text it is rather than
 * half-rendered by a hand-rolled parser. */
function ReadmePanel({
  base,
  org,
  repo,
  refName,
  path,
  name,
}: {
  base: string;
  org: string;
  repo: string;
  refName: string;
  path: string;
  name: string;
}) {
  const full = [path, name].filter(Boolean).join("/");
  const readme = useQuery({
    queryKey: ["blob", org, repo, refName, full],
    queryFn: () => api.text(`${base}/blob/${enc(refName)}/${encodePath(full)}`),
  });
  return (
    <Panel>
      <PanelHead title="README.md" />
      <Async query={readme}>
        {(text) => (
          <pre
            style={{
              margin: 0,
              padding: "14px 16px",
              font: "12px/1.7 var(--mono)",
              color: "var(--fg-dim)",
              whiteSpace: "pre-wrap",
              wordBreak: "break-word",
            }}
          >
            {text}
          </pre>
        )}
      </Async>
    </Panel>
  );
}

const crumb: React.CSSProperties = {
  background: "transparent",
  border: "none",
  color: "var(--link)",
  font: "11px var(--mono)",
  cursor: "pointer",
  padding: 0,
};
