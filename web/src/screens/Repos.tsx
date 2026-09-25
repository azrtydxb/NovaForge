import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import { Async, Empty, Failed, Page, Panel, PanelHead } from "../components/ui";
import { Confirm, Dialog } from "../components/Dialog";
import type { Commit, OrgMember, Ref, TreeEntry, User } from "../lib/types";

/** Repos is the design's browser: a repository, its tree, and a file. The
 * tree is read one directory at a time, which is how git-platform serves it. */
export function Repos() {
  const w = useWorkspace();
  const [repo, setRepo] = useState<string | null>(w.repo);
  const active = repo ?? w.repo ?? w.repos[0]?.name ?? null;
  const [deleting, setDeleting] = useState(false);
  const qc = useQueryClient();

  // Whether to offer deletion is decided from the platform's own record of
  // this person's role, the same role git-platform enforces. Offering the
  // button to a member would only lead them to a refusal.
  const me = useQuery({
    queryKey: ["user"],
    queryFn: () => api.get<User>("/api/v1/user"),
  });
  const members = useQuery({
    queryKey: ["members", w.org],
    queryFn: () =>
      api.get<{ members: OrgMember[] }>(`/api/v1/orgs/${enc(w.org!)}/members`),
    enabled: w.org !== null,
  });
  const myRole = members.data?.members.find(
    (m) => m.user_id === me.data?.id,
  )?.role;
  const canDelete = myRole === "owner" || myRole === "admin";

  const remove = useMutation({
    mutationFn: (name: string) =>
      api.del(`/api/v1/orgs/${enc(w.org!)}/repos/${enc(name)}`),
    onSuccess: (_d, name) => {
      setDeleting(false);
      setRepo(null);
      // A workspace still scoped to the deleted repository would make every
      // screen ask for something that no longer exists.
      if (w.repo === name) w.setRepo(null);
      qc.invalidateQueries({ queryKey: ["repos"] });
    },
  });

  return (
    <Page
      title="Repositories"
      subtitle="Standard Git, browsed through the platform"
      actions={
        active !== null && canDelete ? (
          <button
            onClick={() => {
              remove.reset();
              setDeleting(true);
            }}
            style={dangerButton}
          >
            Delete repository
          </button>
        ) : null
      }
    >
      {deleting && active !== null ? (
        <Confirm
          title={`Delete ${active}`}
          body={
            <>
              This permanently deletes the repository <strong>{active}</strong>{" "}
              and its entire history from this organization. Clones elsewhere
              are unaffected; nothing on the platform can be recovered.
            </>
          }
          confirmLabel="Delete repository"
          typeToConfirm={active}
          danger
          busy={remove.isPending}
          error={remove.error}
          onConfirm={() => remove.mutate(active)}
          onClose={() => setDeleting(false)}
        />
      ) : null}

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
          key={`${w.org}/${active}`}
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
  const [creating, setCreating] = useState(false);
  const qc = useQueryClient();

  const createBranch = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post(`${base}/branches`, { name: v.name, from: v.from }),
    onSuccess: (_d, v) => {
      setCreating(false);
      setBranch(v.name!);
      qc.invalidateQueries({ queryKey: ["branches"] });
    },
  });

  const branches = useQuery({
    queryKey: ["branches", org, repo],
    queryFn: () => api.get<{ refs: Ref[] }>(`${base}/branches`),
  });
  const refs = branches.data?.refs ?? [];
  const tagList = useQuery({
    queryKey: ["tags", org, repo],
    queryFn: () => api.get<{ refs: Ref[] }>(`${base}/tags`),
  });
  const tags = tagList.data?.refs ?? [];
  // A repository with no branch has no commit to read a tree or a log from.
  // Asking anyway answered 404, which the panels showed as "not available in
  // this deployment" — wrong on both counts for an empty repository.
  const empty = branches.data !== undefined && refs.length === 0;

  // The branch shown is the one chosen, else the repository's default, else
  // whatever exists. A repository whose default branch has no commit yet —
  // which is every repository an agent has written to and nobody has pushed
  // to — would otherwise show nothing at all.
  const head =
    (branch && [...refs, ...tags].some((r) => r.name === branch)
      ? branch
      : null) ??
    refs.find((r) => r.name === defaultBranch)?.name ??
    refs[0]?.name ??
    defaultBranch;

  const tree = useQuery({
    queryKey: ["tree", org, repo, head, path],
    queryFn: () =>
      api.get<{ entries: TreeEntry[] }>(
        `${base}/tree/${enc(head)}/${path.split("/").map(enc).join("/")}`,
      ),
    enabled: branches.data !== undefined && !empty,
  });

  // A blob is served as raw bytes, not JSON: a file is bytes, and wrapping it
  // in JSON would mean base64 and a size limit.
  const blob = useQuery({
    queryKey: ["blob", org, repo, head, file],
    queryFn: () =>
      api.text(
        `${base}/blob/${enc(head)}/${file!.split("/").map(enc).join("/")}`,
      ),
    enabled: file !== null,
  });

  const commits = useQuery({
    queryKey: ["commits", org, repo, head],
    queryFn: () =>
      api.get<{ commits: Commit[] }>(`${base}/commits/${enc(head)}?limit=8`),
    enabled: branches.data !== undefined && !empty,
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
              placeholder: head,
              help: "Empty starts from the repository's default branch.",
            },
          ]}
          busy={createBranch.isPending}
          error={createBranch.error}
          onSubmit={(v) => createBranch.mutate(v)}
          onClose={() => setCreating(false)}
        />
      ) : null}
      <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
        <Panel>
          <PanelHead>
            {repo}
            <div style={{ flex: 1 }} />
            <button
              onClick={() => setCreating(true)}
              style={{
                background: "transparent",
                border: "1px solid var(--line-2)",
                borderRadius: 6,
                color: "var(--fg-muted)",
                font: "10px var(--sans)",
                padding: "3px 7px",
                cursor: "pointer",
              }}
            >
              new branch
            </button>
            <select
              aria-label="Branch or tag"
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
              <optgroup label="Branches">
                {refs.map((r) => (
                  <option key={r.name} value={r.name}>
                    {r.name}
                  </option>
                ))}
              </optgroup>
              {tags.length > 0 ? (
                <optgroup label="Tags">
                  {tags.map((r) => (
                    <option key={`tag:${r.name}`} value={r.name}>
                      {r.name}
                    </option>
                  ))}
                </optgroup>
              ) : null}
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
          {branches.error || tagList.error ? (
            <Failed error={branches.error || tagList.error} />
          ) : empty ? (
            <Empty>
              This repository is empty. Push a first commit to {defaultBranch}.
            </Empty>
          ) : (
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
          )}
        </Panel>

        <Panel>
          <PanelHead>RECENT COMMITS</PanelHead>
          {empty ? (
            <Empty>No commits yet.</Empty>
          ) : (
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
          )}
        </Panel>

        {empty ? null : (
          <Compare
            base={base}
            refs={[...refs, ...tags]}
            defaultFrom={defaultBranch}
            defaultTo={head}
          />
        )}
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

/** Compare shows the unified diff between two refs, branches or tags, as
 * git-platform computes it. Nothing is fetched until someone asks, so the
 * panel never shows an empty diff for a question nobody put. */
function Compare({
  base,
  refs,
  defaultFrom,
  defaultTo,
}: {
  base: string;
  refs: Ref[];
  defaultFrom: string;
  defaultTo: string;
}) {
  const [from, setFrom] = useState(defaultFrom);
  const [to, setTo] = useState(defaultTo);
  const [asked, setAsked] = useState<{ from: string; to: string } | null>(null);
  const diff = useQuery({
    queryKey: ["diff", base, asked?.from, asked?.to],
    queryFn: () =>
      api.get<{ unified: string }>(
        `${base}/diff?from=${enc(asked!.from)}&to=${enc(asked!.to)}`,
      ),
    enabled: asked !== null,
  });
  const names = Array.from(new Set(refs.map((r) => r.name)));
  const picker = (value: string, onChange: (v: string) => void) => (
    <select
      aria-label={onChange === setFrom ? "Compare from ref" : "Compare to ref"}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      style={selectStyle}
    >
      {names.includes(value) ? null : <option>{value}</option>}
      {names.map((n) => (
        <option key={n} value={n}>
          {n}
        </option>
      ))}
    </select>
  );
  return (
    <Panel>
      <PanelHead>COMPARE</PanelHead>
      <div
        style={{
          display: "flex",
          gap: 6,
          alignItems: "center",
          flexWrap: "wrap",
          padding: "8px 14px",
        }}
      >
        {picker(from, setFrom)}
        <span style={{ color: "var(--fg-faint)", font: "11px var(--mono)" }}>
          ..
        </span>
        {picker(to, setTo)}
        <button
          onClick={() => setAsked({ from, to })}
          disabled={from === to}
          style={{
            background: "transparent",
            border: "1px solid var(--line-2)",
            borderRadius: 6,
            color: "var(--fg-muted)",
            font: "10px var(--sans)",
            padding: "3px 7px",
            cursor: from === to ? "default" : "pointer",
          }}
        >
          show diff
        </button>
      </div>
      {asked === null ? (
        <Empty>Choose two refs to see what changed between them.</Empty>
      ) : (
        <Async query={diff}>
          {(d) =>
            d.unified === "" ? (
              <Empty>
                No difference between {asked.from} and {asked.to}.
              </Empty>
            ) : (
              <pre
                style={{
                  margin: 0,
                  padding: 14,
                  maxHeight: 360,
                  overflow: "auto",
                  font: "11px/1.6 var(--mono)",
                  whiteSpace: "pre",
                }}
              >
                {d.unified.split("\n").map((line, i) => (
                  <div key={i} style={{ color: diffColor(line) }}>
                    {line || " "}
                  </div>
                ))}
              </pre>
            )
          }
        </Async>
      )}
    </Panel>
  );
}

function diffColor(line: string): string {
  if (line.startsWith("+++") || line.startsWith("---"))
    return "var(--fg-muted)";
  if (line.startsWith("+")) return "var(--ok)";
  if (line.startsWith("-")) return "var(--bad)";
  return "var(--fg-dim)";
}

const selectStyle: React.CSSProperties = {
  background: "var(--bg)",
  border: "1px solid var(--line-2)",
  borderRadius: 6,
  color: "var(--fg-dim)",
  font: "11px var(--mono)",
  padding: "3px 6px",
  outline: "none",
  maxWidth: 160,
};

const dangerButton: React.CSSProperties = {
  padding: "7px 14px",
  background: "transparent",
  border: "1px solid #e5534b66",
  borderRadius: 8,
  color: "var(--bad)",
  font: "600 12px var(--sans)",
  cursor: "pointer",
};

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
