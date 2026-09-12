import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import { Async, Empty, Page, Panel, PanelHead } from "../components/ui";
import type { Commit, Ref, TreeEntry } from "../lib/types";

/** Repos is the design's browser: a repository, its tree, and a file. The
 * tree is read one directory at a time, which is how git-platform serves it. */
export function Repos() {
  const w = useWorkspace();
  const [repo, setRepo] = useState<string | null>(w.repo);
  const active = repo ?? w.repo ?? w.repos[0]?.name ?? null;

  return (
    <Page
      title="Repositories"
      subtitle="Standard Git, browsed through the platform"
    >
      <div
        style={{ display: "flex", gap: 6, marginBottom: 12, flexWrap: "wrap" }}
      >
        {w.repos.map((r) => (
          <button
            key={r.id}
            onClick={() => setRepo(r.name)}
            style={{
              padding: "5px 11px",
              borderRadius: 7,
              border: "1px solid var(--line)",
              background:
                active === r.name ? "rgba(77,127,255,.18)" : "transparent",
              color: active === r.name ? "#fff" : "var(--fg-muted)",
              font: "500 12px var(--sans)",
              cursor: "pointer",
            }}
          >
            {r.name}
          </button>
        ))}
      </div>

      {active === null ? (
        <Panel>
          <Empty>This organization has no repositories yet.</Empty>
        </Panel>
      ) : (
        <Browser
          org={w.org!}
          repo={active}
          defaultBranch={
            w.repos.find((r) => r.name === active)?.default_branch ?? "main"
          }
        />
      )}
    </Page>
  );
}

function Browser({
  org,
  repo,
  defaultBranch,
}: {
  org: string;
  repo: string;
  defaultBranch: string;
}) {
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}`;
  const [path, setPath] = useState("");
  const [file, setFile] = useState<string | null>(null);

  const [branch, setBranch] = useState<string | null>(null);

  const branches = useQuery({
    queryKey: ["branches", org, repo],
    queryFn: () => api.get<{ refs: Ref[] }>(`${base}/branches`),
  });
  const refs = branches.data?.refs ?? [];

  // The branch shown is the one chosen, else the repository's default, else
  // whatever exists. A repository whose default branch has no commit yet —
  // which is every repository an agent has written to and nobody has pushed
  // to — would otherwise show nothing at all.
  const head =
    (branch && refs.some((r) => r.name === branch) ? branch : null) ??
    refs.find((r) => r.name === defaultBranch)?.name ??
    refs[0]?.name ??
    defaultBranch;

  const tree = useQuery({
    queryKey: ["tree", org, repo, head, path],
    queryFn: () =>
      api.get<{ entries: TreeEntry[] }>(`${base}/tree/${enc(head)}/${path}`),
    enabled: branches.data !== undefined,
  });

  // A blob is served as raw bytes, not JSON: a file is bytes, and wrapping it
  // in JSON would mean base64 and a size limit.
  const blob = useQuery({
    queryKey: ["blob", org, repo, head, file],
    queryFn: () => api.text(`${base}/blob/${enc(head)}/${file}`),
    enabled: file !== null,
  });

  const commits = useQuery({
    queryKey: ["commits", org, repo, head],
    queryFn: () =>
      api.get<{ commits: Commit[] }>(`${base}/commits/${enc(head)}?limit=8`),
    enabled: branches.data !== undefined,
  });

  const segments = path ? path.split("/").filter(Boolean) : [];

  return (
    <div
      style={{
        display: "grid",
        gridTemplateColumns: "minmax(0,300px) minmax(0,1fr)",
        gap: 14,
      }}
    >
      <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
        <Panel>
          <PanelHead>
            {repo}
            <div style={{ flex: 1 }} />
            <select
              value={head}
              onChange={(e) => {
                setBranch(e.target.value);
                setPath("");
                setFile(null);
              }}
              style={{
                background: "var(--bg)",
                border: "1px solid var(--line-2)",
                borderRadius: 6,
                color: "var(--fg-dim)",
                font: "11px var(--mono)",
                padding: "3px 6px",
                outline: "none",
                maxWidth: 200,
              }}
            >
              {refs.length === 0 ? <option>{head}</option> : null}
              {refs.map((r) => (
                <option key={r.name} value={r.name}>
                  {r.name}
                </option>
              ))}
            </select>
          </PanelHead>
          <div
            style={{
              padding: "8px 14px",
              borderBottom: "1px solid var(--line)",
              font: "11px var(--mono)",
              color: "var(--fg-muted)",
            }}
          >
            <button
              onClick={() => {
                setPath("");
                setFile(null);
              }}
              style={crumb}
            >
              /
            </button>
            {segments.map((s, i) => (
              <button
                key={i}
                onClick={() => {
                  setPath(segments.slice(0, i + 1).join("/"));
                  setFile(null);
                }}
                style={crumb}
              >
                {s}/
              </button>
            ))}
          </div>
          <Async query={tree}>
            {(d) =>
              d.entries.length === 0 ? (
                <Empty>This repository has no commits yet.</Empty>
              ) : (
                <>
                  {path ? (
                    <button
                      onClick={() => {
                        setPath(segments.slice(0, -1).join("/"));
                        setFile(null);
                      }}
                      style={entryStyle("var(--fg-muted)")}
                    >
                      ../
                    </button>
                  ) : null}
                  {d.entries.map((e) => (
                    <button
                      key={e.name}
                      onClick={() => {
                        const next = path ? `${path}/${e.name}` : e.name;
                        if (e.kind === "tree") {
                          setPath(next);
                          setFile(null);
                        } else {
                          setFile(next);
                        }
                      }}
                      style={entryStyle(
                        e.kind === "tree" ? "var(--fg-dim)" : "var(--link)",
                      )}
                    >
                      {e.kind === "tree" ? "▸ " : "  "}
                      {e.name}
                    </button>
                  ))}
                </>
              )
            }
          </Async>
        </Panel>

        <Panel>
          <PanelHead>RECENT COMMITS</PanelHead>
          <Async query={commits}>
            {(d) =>
              d.commits.length === 0 ? (
                <Empty>No commits.</Empty>
              ) : (
                <>
                  {d.commits.map((c) => (
                    <div
                      key={c.sha}
                      style={{
                        padding: "9px 14px",
                        borderBottom: "1px solid var(--line)",
                      }}
                    >
                      <div style={{ font: "12px var(--sans)" }}>
                        {c.message.split("\n")[0]}
                      </div>
                      <div
                        style={{
                          font: "11px var(--mono)",
                          color: "var(--fg-faint)",
                          marginTop: 3,
                        }}
                      >
                        {c.sha.slice(0, 8)} · {c.author_name}
                      </div>
                    </div>
                  ))}
                </>
              )
            }
          </Async>
        </Panel>
      </div>

      <Panel style={{ minWidth: 0 }}>
        <PanelHead>{file ?? "SELECT A FILE"}</PanelHead>
        {file === null ? (
          <Empty>Pick a file from the tree.</Empty>
        ) : (
          <Async query={blob}>
            {(d) => (
              <pre
                style={{
                  margin: 0,
                  padding: 16,
                  maxHeight: "calc(100vh - 260px)",
                  overflow: "auto",
                  font: "12px/1.65 var(--mono)",
                  color: "var(--fg-dim)",
                  whiteSpace: "pre-wrap",
                  wordBreak: "break-word",
                }}
              >
                {d}
              </pre>
            )}
          </Async>
        )}
      </Panel>
    </div>
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

function entryStyle(color: string): React.CSSProperties {
  return {
    display: "block",
    width: "100%",
    textAlign: "left",
    padding: "6px 14px",
    background: "transparent",
    border: "none",
    color,
    font: "12px var(--mono)",
    cursor: "pointer",
  };
}
