import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import { Async, Empty, Page, Panel, PanelHead } from "../components/ui";

interface Symbol {
  id: string;
  name: string;
  kind: string;
  path: string;
  start_line: number;
  signature: string;
}

/** GraphNode is one end of an edge, as the edge serves it: a symbol, a file,
 * a package or a commit, with only the attributes that kind carries. */
interface GraphNode {
  key: string;
  kind: string;
  name: string;
  path: string;
  symbol_kind: string;
  start_line: string;
  import_path: string;
  external: boolean;
  sha: string;
  author: string;
  message: string;
  changed_at: string;
  work_item_key: string;
}

interface Relations {
  symbol: Symbol | null;
  dependencies: GraphNode[];
  dependents: GraphNode[];
  tests: GraphNode[];
  last_changed_by: string;
  history: GraphNode[];
}

interface FileRelations {
  /** false when nothing is indexed at that path on the default branch. */
  indexed: boolean;
  path: string;
  symbols?: Symbol[];
  imports?: GraphNode[];
  imported_by?: GraphNode[];
  dependents?: GraphNode[];
  tests?: GraphNode[];
  history?: GraphNode[];
}

interface CodeHit {
  path: string;
  start_line: number;
  end_line: number;
  score: number;
  text: string;
}

interface CodeSearch {
  /** "semantic" when results are nearest by meaning, "lexical" when no
   * embedding model answered and the index was searched for the literal text. */
  mode: string;
  results: CodeHit[];
}

const inputStyle = {
  padding: "7px 11px",
  background: "var(--panel)",
  border: "1px solid var(--line-2)",
  borderRadius: 8,
  color: "var(--fg)",
  font: "12px var(--mono)",
  outline: "none",
  width: 260,
} as const;

/** A query names a file when it has a path separator or an extension;
 * anything else is a symbol name. */
function isPath(s: string) {
  return s.includes("/") || s.includes(".");
}

/** Graph answers the questions a file tree cannot: where the code that does
 * something lives, what depends on a symbol or file, what tests cover it, and
 * which commits and Work Items changed it. Every relation shown is an edge
 * the indexer wrote from the default branch; nothing is inferred here. */
export function Graph() {
  const w = useWorkspace();
  const repo = w.repo;
  const [name, setName] = useState("");
  const [submitted, setSubmitted] = useState("");
  const [query, setQuery] = useState("");
  const [searched, setSearched] = useState("");

  const search = useQuery({
    queryKey: ["code-search", w.org, repo, searched],
    queryFn: () =>
      api.get<CodeSearch>(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(repo!)}/search?q=${enc(searched)}`,
      ),
    enabled: w.org !== null && repo !== null && searched !== "",
  });

  const fileMode = isPath(submitted);
  const rel = useQuery({
    queryKey: ["graph", w.org, repo, submitted],
    queryFn: () =>
      api.get<Relations>(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(repo!)}/graph/symbol?name=${enc(submitted)}`,
      ),
    enabled: w.org !== null && repo !== null && submitted !== "" && !fileMode,
  });
  const file = useQuery({
    queryKey: ["graph-file", w.org, repo, submitted],
    queryFn: () =>
      api.get<FileRelations>(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(repo!)}/graph/file?path=${enc(submitted)}`,
      ),
    enabled: w.org !== null && repo !== null && submitted !== "" && fileMode,
  });

  return (
    <Page
      title="Engineering graph"
      subtitle={
        repo
          ? `${repo} — symbols, dependencies, tests and the work that changed them, on the default branch`
          : ""
      }
      actions={
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              setSearched(query.trim());
            }}
          >
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search code by meaning…"
              aria-label="Search code by meaning"
              style={inputStyle}
            />
          </form>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              setSubmitted(name.trim());
            }}
          >
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Symbol name or file path…"
              aria-label="Symbol name or file path"
              style={inputStyle}
            />
          </form>
        </div>
      }
    >
      {searched !== "" && repo !== null ? (
        <div style={{ marginBottom: 14 }}>
          <Async query={search}>
            {(d) => <CodeResults query={searched} data={d} />}
          </Async>
        </div>
      ) : null}
      {repo === null ? (
        <Panel>
          <Empty>Select a repository in the workspace switcher.</Empty>
        </Panel>
      ) : submitted === "" ? (
        <Panel>
          <Empty>
            Name a symbol, or a file path, to see what it relates to.
            <br />
            The graph is built from the default branch as commits reach it.
          </Empty>
        </Panel>
      ) : fileMode ? (
        <Async query={file}>
          {(d) => <FileView data={d} onPick={(s) => pick(s)} />}
        </Async>
      ) : (
        <Async query={rel}>
          {(d) => <SymbolView name={submitted} data={d} />}
        </Async>
      )}
    </Page>
  );

  function pick(s: string) {
    setName(s);
    setSubmitted(s);
  }
}

function SymbolView({ name, data: d }: { name: string; data: Relations }) {
  if (d.symbol === null) {
    return (
      <Panel>
        <Empty>No symbol named “{name}” is indexed in this repository.</Empty>
      </Panel>
    );
  }
  return (
    <div style={{ display: "grid", gap: 14 }}>
      <Panel style={{ padding: 16 }}>
        <div style={{ font: "600 16px var(--mono)", color: "var(--link)" }}>
          {d.symbol.name}
        </div>
        <div
          style={{
            font: "12px var(--mono)",
            color: "var(--fg-muted)",
            marginTop: 5,
          }}
        >
          {d.symbol.kind} · {d.symbol.path}:{d.symbol.start_line}
        </div>
        {d.symbol.signature ? (
          <pre
            style={{
              margin: "10px 0 0",
              font: "12px var(--mono)",
              color: "var(--fg-dim)",
              whiteSpace: "pre-wrap",
            }}
          >
            {d.symbol.signature}
          </pre>
        ) : null}
      </Panel>
      <div style={gridStyle}>
        <Relation title="DEPENDS ON" items={d.dependencies.map(label)} />
        <Relation title="DEPENDED ON BY" items={d.dependents.map(label)} />
        <Relation title="TESTED BY" items={d.tests.map(label)} />
        <Relation
          title="LAST CHANGED BY"
          items={d.last_changed_by ? [d.last_changed_by] : []}
          empty={
            d.history.length > 0
              ? "its last commit names no Work Item"
              : undefined
          }
        />
      </div>
      <History nodes={d.history} />
    </div>
  );
}

function FileView({
  data: d,
  onPick,
}: {
  data: FileRelations;
  onPick: (s: string) => void;
}) {
  if (!d.indexed) {
    return (
      <Panel>
        <Empty>
          No file “{d.path}” is indexed on this repository’s default branch.
        </Empty>
      </Panel>
    );
  }
  const symbols = d.symbols ?? [];
  return (
    <div style={{ display: "grid", gap: 14 }}>
      <Panel>
        <PanelHead>
          {d.path}
          <span style={{ color: "var(--fg-faint)" }}>
            {symbols.length} symbols
          </span>
        </PanelHead>
        {symbols.length === 0 ? (
          <Empty>No symbols are defined in this file.</Empty>
        ) : (
          symbols.map((s) => (
            <button
              key={s.id}
              onClick={() => onPick(s.name)}
              style={{
                display: "block",
                width: "100%",
                textAlign: "left",
                padding: "8px 14px",
                background: "none",
                border: "none",
                borderBottom: "1px solid var(--line)",
                font: "12px var(--mono)",
                color: "var(--link)",
                cursor: "pointer",
              }}
            >
              {s.name}
              <span style={{ color: "var(--fg-faint)" }}>
                {" "}
                {s.kind} · line {s.start_line}
              </span>
            </button>
          ))
        )}
      </Panel>
      <div style={gridStyle}>
        <Relation title="IMPORTS" items={(d.imports ?? []).map(label)} />
        <Relation
          title="IMPORTED BY"
          items={(d.imported_by ?? []).map(label)}
        />
        <Relation
          title="ITS SYMBOLS ARE USED BY"
          items={(d.dependents ?? []).map(label)}
        />
        <Relation title="TESTED BY" items={(d.tests ?? []).map(label)} />
      </div>
      <History nodes={d.history ?? []} />
    </div>
  );
}

const gridStyle = {
  display: "grid",
  gridTemplateColumns: "repeat(auto-fit,minmax(240px,1fr))",
  gap: 12,
} as const;

/** label renders a node the way a reader looks for it: a symbol by where it
 * is, a package by its import path, a commit by its Work Item and subject. */
function label(n: GraphNode): string {
  switch (n.kind) {
    case "package":
      return n.external
        ? `${n.import_path} (outside the repository)`
        : n.import_path;
    case "commit":
      return `${n.work_item_key || n.sha.slice(0, 10)} — ${n.message}`;
    case "file":
      return n.path;
    default:
      return n.name ? `${n.path} · ${n.name}` : n.path || n.key;
  }
}

function History({ nodes }: { nodes: GraphNode[] }) {
  return (
    <Panel>
      <PanelHead>
        CHANGE HISTORY
        <span style={{ color: "var(--fg-faint)" }}>{nodes.length}</span>
      </PanelHead>
      {nodes.length === 0 ? (
        <Empty>No indexed commit on the default branch changed this.</Empty>
      ) : (
        nodes.map((c) => (
          <div
            key={c.key}
            style={{
              padding: "8px 14px",
              borderBottom: "1px solid var(--line)",
              display: "flex",
              gap: 12,
              alignItems: "baseline",
              font: "12px var(--mono)",
            }}
          >
            <span style={{ color: "var(--link)", minWidth: 60 }}>
              {c.work_item_key || "—"}
            </span>
            <span style={{ color: "var(--fg-dim)", flex: 1 }}>{c.message}</span>
            <span style={{ color: "var(--fg-faint)" }}>
              {c.author} · {c.sha.slice(0, 10)} · {c.changed_at.slice(0, 10)}
            </span>
          </div>
        ))
      )}
    </Panel>
  );
}

function CodeResults({ query, data }: { query: string; data: CodeSearch }) {
  const semantic = data.mode === "semantic";
  return (
    <Panel>
      <PanelHead>
        CODE MATCHING “{query}”
        <span style={{ color: "var(--fg-faint)" }}>
          {data.results.length} · {semantic ? "by meaning" : "literal text"}
        </span>
      </PanelHead>
      {semantic ? null : (
        // The platform answered, but not the question asked: say so rather
        // than let a substring match pass for a semantic one.
        <div
          style={{
            padding: "8px 14px",
            borderBottom: "1px solid var(--line)",
            font: "12px var(--sans)",
            color: "var(--warn)",
          }}
        >
          No embedding model answered, so this is a literal text match, not a
          search by meaning.
        </div>
      )}
      {data.results.length === 0 ? (
        <Empty>
          Nothing indexed matches.
          <br />
          Code is indexed as it reaches the default branch.
        </Empty>
      ) : (
        data.results.map((r) => (
          <div
            key={`${r.path}:${r.start_line}`}
            style={{
              padding: "8px 14px",
              borderBottom: "1px solid var(--line)",
              display: "flex",
              gap: 12,
              alignItems: "baseline",
            }}
          >
            <span
              style={{
                font: "12px var(--mono)",
                color: "var(--link)",
                flex: 1,
                wordBreak: "break-all",
              }}
            >
              {r.path}:{r.start_line}–{r.end_line}
            </span>
            {semantic ? (
              <span
                title="Cosine similarity between the query and this code"
                style={{ font: "11px var(--mono)", color: "var(--fg-muted)" }}
              >
                {r.score.toFixed(3)}
              </span>
            ) : null}
          </div>
        ))
      )}
    </Panel>
  );
}

function Relation({
  title,
  items,
  empty,
}: {
  title: string;
  items: string[];
  empty?: string | undefined;
}) {
  return (
    <Panel>
      <PanelHead>
        {title}
        <span style={{ color: "var(--fg-faint)" }}>{items.length}</span>
      </PanelHead>
      {items.length === 0 ? (
        <Empty>{empty ?? "—"}</Empty>
      ) : (
        items.map((i) => (
          <div
            key={i}
            style={{
              padding: "8px 14px",
              borderBottom: "1px solid var(--line)",
              font: "12px var(--mono)",
              color: "var(--fg-dim)",
              wordBreak: "break-all",
            }}
          >
            {i}
          </div>
        ))
      )}
    </Panel>
  );
}
