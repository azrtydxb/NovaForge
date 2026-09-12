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

/** Graph answers the questions a file tree cannot: what depends on this, what
 * tests cover it, which Work Item last changed it. */
export function Graph() {
  const w = useWorkspace();
  const repo = w.repo ?? w.repos[0]?.name ?? null;
  const [name, setName] = useState("");
  const [submitted, setSubmitted] = useState("");

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
            style={{
              padding: "7px 11px",
              background: "var(--panel)",
              border: "1px solid var(--line-2)",
              borderRadius: 8,
              color: "var(--fg)",
              font: "12px var(--mono)",
              outline: "none",
              width: 260,
            }}
          />
        </form>
      }
    >
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
