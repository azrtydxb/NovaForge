import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { useWorkspace } from "../lib/workspace";
import { Async, Empty, Page, Panel, Pill } from "../components/ui";
import { Dialog, NewButton } from "../components/Dialog";

interface KnowledgeEntry {
  id: string;
  key: string;
  kind: string;
  title: string;
  body: string;
  created_at: string;
  /** The Agent Run that recorded the entry; empty when a person did. */
  source_run_id: string;
}

interface KnowledgeAnswer {
  entries: KnowledgeEntry[];
  /** "recent" with no query, "semantic" when an embedding model answered,
   * "text" when the entries were matched by their words. */
  mode: string;
}

const KINDS = ["decision", "correction", "pattern", "incident", "operational"];

const KIND_TONE: Record<string, string> = {
  decision: "var(--link)",
  pattern: "var(--ok)",
  incident: "var(--bad)",
  correction: "var(--warn)",
  operational: "var(--violet)",
};

/** Knowledge is the project's own memory: decisions, patterns, incidents and
 * corrections that outlive the run that produced them, and that later agent
 * runs on this repository are given before their first turn. A repository
 * must be selected — knowledge is per-repository, because a decision about
 * one codebase is not a decision about another. */
export function Knowledge() {
  const w = useWorkspace();
  const qc = useQueryClient();
  const [q, setQ] = useState("");
  const [recording, setRecording] = useState(false);
  const repo = w.repo;

  const entries = useQuery({
    queryKey: ["knowledge", w.org, repo, q],
    queryFn: () =>
      api.get<KnowledgeAnswer>(
        `/api/v1/orgs/${enc(w.org!)}/repos/${enc(repo!)}/knowledge?q=${enc(q)}`,
      ),
    enabled: w.org !== null && repo !== null,
  });

  const record = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post(`/api/v1/orgs/${enc(w.org!)}/repos/${enc(repo!)}/knowledge`, {
        kind: v.kind,
        title: v.title,
        body: v.body,
      }),
    onSuccess: () => {
      setRecording(false);
      qc.invalidateQueries({ queryKey: ["knowledge"] });
    },
  });

  return (
    <Page
      title="Knowledge"
      subtitle={
        repo
          ? `${repo} — what this project has learned, given to every later agent run it is relevant to`
          : "Select a repository"
      }
      actions={
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search knowledge…"
            aria-label="Search knowledge"
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
          {repo !== null ? (
            <NewButton
              label="Record knowledge"
              onClick={() => setRecording(true)}
            />
          ) : null}
        </div>
      }
    >
      {recording ? (
        <Dialog
          title="Record project knowledge"
          description="Later agent runs on this repository are given entries related to their work item. Recording again with the same kind and title revises the entry."
          submitLabel="Record"
          fields={[
            {
              name: "kind",
              label: "Kind",
              type: "select",
              options: KINDS,
            },
            {
              name: "title",
              label: "Title",
              required: true,
              placeholder: "VAT is rounded per invoice line",
            },
            {
              name: "body",
              label: "What was decided, and why",
              type: "textarea",
              required: true,
            },
          ]}
          busy={record.isPending}
          error={record.error}
          onSubmit={(v) => record.mutate(v)}
          onClose={() => setRecording(false)}
        />
      ) : null}
      {repo === null ? (
        <Panel>
          <Empty>Select a repository in the workspace switcher.</Empty>
        </Panel>
      ) : (
        <Async query={entries}>
          {(d) => (
            <div style={{ display: "grid", gap: 10 }}>
              {q !== "" && d.mode === "text" ? (
                // The platform answered by words, not by meaning; say so
                // rather than let a word match pass for a semantic one.
                <div style={{ font: "12px var(--sans)", color: "var(--warn)" }}>
                  No embedding model answered, so these entries share words with
                  the search rather than its meaning.
                </div>
              ) : null}
              {d.entries.length === 0 ? (
                <Panel>
                  <Empty>
                    {q
                      ? `Nothing recorded matches “${q}”.`
                      : "This repository has recorded no knowledge yet."}
                    <br />
                    Agents record decisions with knowledge.record as they work;
                    people record them here.
                  </Empty>
                </Panel>
              ) : (
                d.entries.map((k) => <Entry key={k.id} entry={k} />)
              )}
            </div>
          )}
        </Async>
      )}
    </Page>
  );
}

function Entry({ entry: k }: { entry: KnowledgeEntry }) {
  const tone = KIND_TONE[k.kind] ?? "var(--fg-muted)";
  return (
    <Panel style={{ padding: 14 }}>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 10,
          flexWrap: "wrap",
        }}
      >
        <span style={{ font: "600 12px var(--mono)", color: "var(--link)" }}>
          {k.key}
        </span>
        <Pill bg={`${tone}22`} fg={tone}>
          {k.kind}
        </Pill>
        <div style={{ flex: 1 }} />
        <span style={{ font: "10px var(--mono)", color: "var(--fg-faint)" }}>
          {k.source_run_id
            ? `recorded by agent run ${k.source_run_id.slice(0, 8)}`
            : "recorded by a person"}
          {" · "}
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
          whiteSpace: "pre-wrap",
        }}
      >
        {k.body}
      </div>
    </Panel>
  );
}
