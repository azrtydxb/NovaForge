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

interface Relations {
  symbol: Symbol | null;
  dependencies: { key: string; kind: string }[];
  dependents: { key: string; kind: string }[];
  tests: string[];
  last_changed_by: string;
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

/** Graph answers the questions a file tree cannot: where the code that does
 * something lives, what depends on a symbol, what tests cover it, which Work
 * Item last changed it. */
export function Graph() {
  const w = useWorkspace();
  const repo = w.repo ?? w.repos[0]?.name ?? null;
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

  const rel = useQuery({
    queryKey: ["graph", w.org, repo, submitted],
    queryFn: () =>
      api.get<Relations>(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(repo!)}/graph/symbol?name=${enc(submitted)}`,
      ),
    enabled: w.org !== null && repo !== null && submitted !== "",
  });

  return (
    <Page
      title="Engineering graph"
      subtitle={
        repo
          ? `${repo} — symbols, dependencies, tests and the work that changed them`
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
              placeholder="Symbol name…"
              aria-label="Symbol name"
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
      {submitted === "" ? (
        <Panel>
          <Empty>
            Name a symbol to see what it relates to.
            <br />
            The graph is built as commits are indexed.
          </Empty>
        </Panel>
      ) : (
        <Async query={rel}>
          {(d) =>
            d.symbol === null ? (
              <Panel>
                <Empty>
                  No symbol named “{submitted}” is indexed in this repository.
                </Empty>
              </Panel>
            ) : (
              <div style={{ display: "grid", gap: 14 }}>
                <Panel style={{ padding: 16 }}>
                  <div
                    style={{
                      font: "600 16px var(--mono)",
                      color: "var(--link)",
                    }}
                  >
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

                <div
                  style={{
                    display: "grid",
                    gridTemplateColumns: "repeat(auto-fit,minmax(240px,1fr))",
                    gap: 12,
                  }}
                >
                  <Relation
                    title="DEPENDS ON"
                    items={d.dependencies.map((x) => x.key)}
                  />
                  <Relation
                    title="DEPENDED ON BY"
                    items={d.dependents.map((x) => x.key)}
                  />
                  <Relation title="TESTED BY" items={d.tests} />
                  <Relation
                    title="LAST CHANGED BY"
                    items={d.last_changed_by ? [d.last_changed_by] : []}
                  />
                </div>
              </div>
            )
          }
        </Async>
      )}
    </Page>
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
          Code is indexed as it is pushed.
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

function Relation({ title, items }: { title: string; items: string[] }) {
  return (
    <Panel>
      <PanelHead>
        {title}
        <span style={{ color: "var(--fg-faint)" }}>{items.length}</span>
      </PanelHead>
      {items.length === 0 ? (
        <Empty>—</Empty>
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
