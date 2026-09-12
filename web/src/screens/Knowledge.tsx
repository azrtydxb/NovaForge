import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import { Async, Empty, Page, Panel, Pill } from "../components/ui";

interface KnowledgeEntry {
  id: string;
  key: string;
  kind: string;
  title: string;
  body: string;
  created_at: string;
}

const KIND_TONE: Record<string, string> = {
  decision: "var(--link)",
  pattern: "var(--ok)",
  incident: "var(--bad)",
  correction: "var(--warn)",
  operational: "var(--violet)",
};

/** Knowledge is the project's own memory: decisions, patterns, incidents and
 * corrections that outlive the run that produced them. A repository must be
 * selected — knowledge is per-repository, because a decision about one
 * codebase is not a decision about another. */
export function Knowledge() {
  const w = useWorkspace();
  const [q, setQ] = useState("");
  const repo = w.repo ?? w.repos[0]?.name ?? null;

  const entries = useQuery({
    queryKey: ["knowledge", w.org, repo, q],
    queryFn: () =>
      api.get<{ entries: KnowledgeEntry[] }>(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(repo!)}/knowledge?q=${enc(q)}`,
      ),
    enabled: w.org !== null && repo !== null,
  });

  return (
    <Page
      title="Knowledge"
      subtitle={
        repo
          ? `${repo} — what this project has learned, kept as evidence rather than as chat history`
          : "Select a repository"
      }
      actions={
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search knowledge…"
          style={{
            padding: "7px 11px",
            background: "var(--panel)",
            border: "1px solid var(--line-2)",
            borderRadius: 8,
            color: "var(--fg)",
            font: "12px var(--sans)",
            outline: "none",
            width: 240,
          }}
        />
      }
    >
      {repo === null ? (
        <Panel>
          <Empty>This organization has no repositories.</Empty>
        </Panel>
      ) : (
        <Async query={entries}>
          {(d) =>
            d.entries.length === 0 ? (
              <Panel>
                <Empty>
                  {q
                    ? `Nothing recorded matches “${q}”.`
                    : "This repository has recorded no knowledge yet."}
                  <br />
                  Agents record decisions and corrections as they work; people
                  record them when they correct an agent.
                </Empty>
              </Panel>
            ) : (
              <div style={{ display: "grid", gap: 10 }}>
                {d.entries.map((k) => (
                  <Panel key={k.id} style={{ padding: 14 }}>
                    <div
                      style={{ display: "flex", alignItems: "center", gap: 10 }}
                    >
                      <span
                        style={{
                          font: "600 12px var(--mono)",
                          color: "var(--link)",
                        }}
                      >
                        {k.key}
                      </span>
                      <Pill
                        bg={`${KIND_TONE[k.kind] ?? "var(--fg-muted)"}22`}
                        fg={KIND_TONE[k.kind] ?? "var(--fg-muted)"}
                      >
                        {k.kind}
                      </Pill>
                      <div style={{ flex: 1 }} />
                      <span
                        style={{
                          font: "10px var(--mono)",
                          color: "var(--fg-faint)",
                        }}
                      >
                        {k.created_at.slice(0, 10)}
                      </span>
                    </div>
                    <div style={{ font: "600 14px var(--sans)", marginTop: 8 }}>
                      {k.title}
                    </div>
                    <div
                      style={{
                        font: "13px/1.6 var(--sans)",
                        color: "var(--fg-dim)",
                        marginTop: 6,
                      }}
                    >
                      {k.body}
                    </div>
                  </Panel>
                ))}
              </div>
            )
          }
        </Async>
      )}
    </Page>
  );
}
