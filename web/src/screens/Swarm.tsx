import { useQueries } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { scopedRepos, useWorkspace } from "../lib/workspace";
import {
  Empty,
  Failed,
  Loading,
  Page,
  Panel,
  PanelHead,
  StatePill,
} from "../components/ui";
import type { Subtask, WorkItem } from "../lib/types";

/** Swarm shows an epic's decomposition as the design's waves: what can start
 * now, and what is still waiting on something. A subtask's readiness is the
 * platform's own answer (work.Store.Ready), not a guess made here — the whole
 * point of dependency ordering is that one component decides it. */
export function Swarm() {
  const w = useWorkspace();
  const repos = scopedRepos(w);

  const work = useQueries({
    queries: repos.map((r) => ({
      queryKey: ["work", w.org, r.name],
      queryFn: () =>
        api.get<{ items: WorkItem[] }>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(r.name)}/work`,
        ),
      enabled: w.org !== null,
    })),
  });

  const epics = work.flatMap((q, i) =>
    (q.data?.items ?? [])
      .filter((it) => it.state !== "done")
      .map((item) => ({ item, repo: repos[i]!.name })),
  );

  const subtasks = useQueries({
    queries: epics.map(({ item, repo }) => ({
      queryKey: ["subtasks", w.org, repo, item.key],
      queryFn: () =>
        api.get<{ subtasks: Subtask[] }>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(repo)}/work/${enc(item.key)}/subtasks`,
        ),
      enabled: w.org !== null,
    })),
  });

  const loading =
    work.some((q) => q.isLoading) || subtasks.some((q) => q.isLoading);

  const listError = [...work, ...subtasks].find((q) => q.error)?.error;

  const decomposed = epics
    .map((e, i) => ({ ...e, subtasks: subtasks[i]?.data?.subtasks ?? [] }))
    .filter((e) => e.subtasks.length > 0);

  return (
    <Page
      title="Swarm"
      subtitle="Epics broken into dependency-ordered subtasks across specialized agents"
    >
      {loading ? (
        <Panel>
          <Loading />
        </Panel>
      ) : listError ? (
        <Failed error={listError} />
      ) : decomposed.length === 0 ? (
        <Panel>
          <Empty>
            Nothing is decomposed in this scope.
            <br />
            Decompose an epic with{" "}
            <code style={{ font: "11px var(--mono)" }}>
              nf work decompose &lt;repo&gt; &lt;key&gt;
            </code>
            .
          </Empty>
        </Panel>
      ) : (
        decomposed.map((e) => (
          <div key={e.item.id} style={{ marginBottom: 18 }}>
            <div
              style={{
                display: "flex",
                alignItems: "center",
                gap: 10,
                marginBottom: 8,
              }}
            >
              <span
                style={{ font: "600 13px var(--mono)", color: "var(--link)" }}
              >
                {e.item.key}
              </span>
              <span style={{ font: "14px var(--sans)" }}>{e.item.goal}</span>
              <span
                style={{ font: "11px var(--mono)", color: "var(--fg-faint)" }}
              >
                {e.repo}
              </span>
            </div>
            <div
              style={{
                display: "grid",
                gridTemplateColumns: "repeat(auto-fit,minmax(260px,1fr))",
                gap: 12,
              }}
            >
              <Wave
                title="READY"
                hint="no unfinished dependencies"
                items={e.subtasks.filter((s) => s.ready && s.state !== "done")}
              />
              <Wave
                title="BLOCKED"
                hint="waiting on another subtask"
                items={e.subtasks.filter((s) => !s.ready && s.state !== "done")}
              />
              <Wave
                title="DONE"
                hint=""
                items={e.subtasks.filter((s) => s.state === "done")}
              />
            </div>
          </div>
        ))
      )}
    </Page>
  );
}

function Wave({
  title,
  hint,
  items,
}: {
  title: string;
  hint: string;
  items: Subtask[];
}) {
  return (
    <Panel>
      <PanelHead>
        {title}
        <span style={{ color: "var(--fg-faint)" }}>{items.length}</span>
        <div style={{ flex: 1 }} />
        {hint ? (
          <span style={{ font: "10px var(--sans)", color: "var(--fg-faint)" }}>
            {hint}
          </span>
        ) : null}
      </PanelHead>
      {items.length === 0 ? (
        <Empty>—</Empty>
      ) : (
        items.map((s) => (
          <div
            key={s.id}
            style={{
              padding: "10px 14px",
              borderBottom: "1px solid var(--line)",
            }}
          >
            <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
              <span
                style={{ font: "600 11px var(--mono)", color: "var(--link)" }}
              >
                {s.key}
              </span>
              <div style={{ flex: 1 }} />
              <StatePill state={s.state} />
            </div>
            <div style={{ font: "12px var(--sans)", marginTop: 5 }}>
              {s.goal}
            </div>
            <div
              style={{
                font: "10px var(--mono)",
                color: "var(--fg-faint)",
                marginTop: 4,
              }}
            >
              {s.assignee_kind === "agent" ? "agent" : s.type}
            </div>
          </div>
        ))
      )}
    </Panel>
  );
}
