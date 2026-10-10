import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useQueries, useQuery } from "@tanstack/react-query";
import { api, enc } from "../lib/api";
import { scopedRepos, useWorkspace } from "../lib/workspace";
import {
  Empty,
  Failed,
  Loading,
  Panel,
  PanelHead,
  StatePill,
} from "../components/ui";
import { timeAgo } from "./Home";
import type {
  EngineeringRun,
  OrgMember,
  Team,
  User,
  WorkItem,
} from "../lib/types";

/** Profile is the design's user profile page. The platform has no public
 * profile endpoint and no per-user activity feed, so the page is built from
 * what exists: the signed-in user, their membership and teams in the current
 * organization, and the org's run and Work Item lists filtered to what this
 * person authored or is assigned. What the design draws that no endpoint
 * answers — a contributions calendar, directed agents, pinned repositories —
 * is named as unavailable rather than invented. */
export function Profile() {
  const w = useWorkspace();

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

  const teams = useQuery({
    // The same key the People & teams screen reads, so moving between the
    // profile and that screen does not refetch what is already in the cache.
    queryKey: ["teams", w.org],
    queryFn: () =>
      api.get<{ teams: Team[] }>(`/api/v1/orgs/${enc(w.org!)}/teams`),
    enabled: w.org !== null,
  });

  const repos = scopedRepos(w);
  const runQueries = useQueries({
    queries: repos.map((r) => ({
      queryKey: ["runs", w.org, r.name],
      queryFn: () =>
        api.get<{ runs: EngineeringRun[] }>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(r.name)}/runs`,
        ),
      enabled: w.org !== null,
    })),
  });
  const workQueries = useQueries({
    queries: repos.map((r) => ({
      queryKey: ["work", w.org, r.name],
      queryFn: () =>
        api.get<{ items: WorkItem[] }>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(r.name)}/work`,
        ),
      enabled: w.org !== null,
    })),
  });

  const myId = me.data?.id;
  const runs = useMemo(
    () =>
      runQueries
        .flatMap((q, i) =>
          (q.data?.runs ?? []).map((run) => ({
            run,
            repo: repos[i]?.name ?? "",
          })),
        )
        .filter((x) => myId !== undefined && x.run.author_id === myId)
        .sort((a, b) => b.run.created_at.localeCompare(a.run.created_at)),
    // The memo keys on the query results themselves: the repos array is a
    // stable view of the workspace, so listing it would change nothing.
    [runQueries, myId],
  );
  const work = useMemo(
    () =>
      workQueries
        .flatMap((q, i) =>
          (q.data?.items ?? []).map((item) => ({
            item,
            repo: repos[i]?.name ?? "",
          })),
        )
        .filter((x) => myId !== undefined && x.item.assignee_id === myId)
        .sort((a, b) => b.item.created_at.localeCompare(a.item.created_at)),
    // Same reasoning as the runs memo above.
    [workQueries, myId],
  );

  const [filter, setFilter] = useState<"all" | "runs" | "work">("all");
  // A run row and a Work Item row carry what their screen shows; `at` is the
  // field the merged feed sorts on. The union, not optional fields, because a
  // run row has no item and a Work Item row has no run.
  type FeedRow =
    | { kind: "run"; at: string; run: EngineeringRun; repo: string }
    | { kind: "work"; at: string; item: WorkItem; repo: string };
  const feed = useMemo<FeedRow[]>(() => {
    const rows: FeedRow[] = [];
    if (filter !== "work") {
      for (const x of runs)
        rows.push({
          kind: "run",
          at: x.run.created_at,
          run: x.run,
          repo: x.repo,
        });
    }
    if (filter !== "runs") {
      for (const x of work)
        rows.push({
          kind: "work",
          at: x.item.created_at,
          item: x.item,
          repo: x.repo,
        });
    }
    return rows.sort((a, b) => b.at.localeCompare(a.at)).slice(0, 12);
  }, [filter, runs, work]);

  const myTeams = (teams.data?.teams ?? []).filter((t) =>
    t.member_ids.includes(myId ?? "___none___"),
  );
  const others = (members.data?.members ?? []).filter(
    (m) => m.user_id !== myId,
  );
  const role = members.data?.members.find((m) => m.user_id === myId)?.role;

  const activityLoading =
    me.isLoading ||
    (repos.length > 0 &&
      (runQueries.some((q) => q.isLoading) ||
        workQueries.some((q) => q.isLoading)));

  return (
    <div style={{ flex: 1, overflowY: "auto", padding: "22px 26px" }}>
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "minmax(0,300px) minmax(0,1fr)",
          gap: 14,
          alignItems: "start",
        }}
      >
        {/* The About card. The design draws a bio, pronouns, links and local
            time here; the platform stores none of those fields, so the card
            shows only what /api/v1/user and the membership say. */}
        <Panel>
          <div
            style={{
              padding: "20px 16px",
              borderBottom: "1px solid var(--line)",
              display: "grid",
              justifyItems: "center",
              textAlign: "center",
              gap: 6,
            }}
          >
            <span
              aria-hidden="true"
              style={{
                width: 64,
                height: 64,
                borderRadius: 99,
                background: "var(--accent-soft)",
                color: "var(--accent)",
                display: "grid",
                placeItems: "center",
                font: "600 22px var(--sans)",
              }}
            >
              {(me.data?.username ?? "?").slice(0, 2).toUpperCase()}
            </span>
            <div>
              <div style={{ font: "600 16px var(--sans)", color: "var(--fg)" }}>
                {me.isLoading ? <Loading /> : me.data?.username}
              </div>
              <div
                style={{
                  font: "12px var(--mono)",
                  color: "var(--fg-muted)",
                  marginTop: 2,
                }}
              >
                @{me.data?.username}
              </div>
            </div>
            <div style={{ font: "12px var(--sans)", color: "var(--fg-dim)" }}>
              {me.data?.email}
            </div>
            {role ? (
              <div style={{ font: "12px var(--sans)", color: "var(--fg-dim)" }}>
                {role} · in {w.org}
              </div>
            ) : null}
            <Link
              to="/account"
              style={{
                marginTop: 6,
                padding: "5px 12px",
                borderRadius: 7,
                border: "1px solid var(--line-2)",
                font: "500 11px var(--sans)",
              }}
            >
              Account settings
            </Link>
          </div>
          <div
            style={{
              padding: "12px 16px",
              borderBottom: "1px solid var(--line)",
            }}
          >
            <div
              style={{
                font: "600 10px var(--sans)",
                letterSpacing: ".08em",
                color: "var(--fg-muted)",
                marginBottom: 8,
              }}
            >
              TEAMS
            </div>
            {myTeams.length === 0 ? (
              <div
                style={{ font: "12px var(--sans)", color: "var(--fg-faint)" }}
              >
                Not a member of any team in {w.org}.
              </div>
            ) : (
              myTeams.map((t) => (
                <div
                  key={t.id}
                  style={{
                    display: "flex",
                    gap: 8,
                    alignItems: "baseline",
                    padding: "4px 0",
                  }}
                >
                  <span
                    style={{ font: "600 12px var(--sans)", color: "var(--fg)" }}
                  >
                    {t.name}
                  </span>
                  <span
                    style={{
                      font: "11px var(--mono)",
                      color: "var(--fg-faint)",
                    }}
                  >
                    grants {t.role} · {t.member_ids.length}{" "}
                    {t.member_ids.length === 1 ? "member" : "members"}
                  </span>
                </div>
              ))
            )}
          </div>
          {others.length > 0 ? (
            <div style={{ padding: "12px 16px" }}>
              <div
                style={{
                  font: "600 10px var(--sans)",
                  letterSpacing: ".08em",
                  color: "var(--fg-muted)",
                }}
              >
                WORKS WITH
              </div>
              <div
                style={{
                  display: "flex",
                  gap: 6,
                  flexWrap: "wrap",
                  marginTop: 8,
                }}
              >
                {others.map((m) => (
                  <span
                    key={m.user_id}
                    title={`${m.username} · ${m.role}`}
                    style={{
                      width: 26,
                      height: 26,
                      borderRadius: 99,
                      background: "var(--panel-2)",
                      border: "1px solid var(--line)",
                      display: "grid",
                      placeItems: "center",
                      font: "600 10px var(--sans)",
                      color: "var(--fg-muted)",
                    }}
                  >
                    {m.username.slice(0, 1).toUpperCase()}
                  </span>
                ))}
              </div>
            </div>
          ) : null}
        </Panel>
        {/* The activity column. The design also draws a contributions
            calendar here plus run/review/work/approval tallies and pinned
            repositories; the platform keeps none of those per user, so the
            panel says so instead of showing a plausible number. */}
        <div style={{ display: "grid", gap: 14 }}>
          <Panel>
            <PanelHead
              title="Recent activity"
              count={runs.length + work.length}
            >
              <div style={{ display: "flex", gap: 4 }}>
                {(["all", "runs", "work"] as const).map((f) => (
                  <button
                    key={f}
                    onClick={() => setFilter(f)}
                    aria-pressed={filter === f}
                    style={{
                      padding: "3px 10px",
                      borderRadius: 99,
                      border: `1px solid ${filter === f ? "var(--accent)" : "var(--line)"}`,
                      background:
                        filter === f ? "var(--accent-soft)" : "transparent",
                      color: filter === f ? "var(--accent)" : "var(--fg-muted)",
                      font: "500 11px var(--sans)",
                      cursor: "pointer",
                    }}
                  >
                    {f === "all" ? "All" : f === "runs" ? "Runs" : "Work items"}
                  </button>
                ))}
              </div>
            </PanelHead>
            {activityLoading ? <Loading /> : null}
            {me.error ? <Failed error={me.error} /> : null}
            {!activityLoading && !me.error ? (
              feed.length === 0 ? (
                <Empty>
                  {filter === "all"
                    ? "No runs authored and no Work Items assigned to you in this scope."
                    : filter === "runs"
                      ? "No runs authored by you in this scope."
                      : "No Work Items assigned to you in this scope."}
                </Empty>
              ) : (
                feed.map((row) =>
                  row.kind === "run" ? (
                    <RunRow key={row.run.id} run={row.run} repo={row.repo} />
                  ) : (
                    <WorkRow
                      key={row.item.id}
                      item={row.item}
                      repo={row.repo}
                    />
                  ),
                )
              )
            ) : null}
            <div
              style={{
                padding: "10px 14px",
                font: "11px var(--sans)",
                color: "var(--fg-faint)",
                lineHeight: 1.6,
              }}
            >
              Contributions, review and approval tallies are not recorded per
              person in this deployment, so this feed counts only what the run
              and Work Item lists answer for.
            </div>
          </Panel>
        </div>
      </div>
    </div>
  );
}

/** One run this person authored, as the activity feed draws it: what it is,
 * where it lives, and where it stands. */
function RunRow({ run, repo }: { run: EngineeringRun; repo: string }) {
  return (
    <div
      style={{
        display: "flex",
        alignItems: "baseline",
        gap: 10,
        padding: "8px 14px",
        borderBottom: "1px solid var(--line)",
      }}
    >
      <span
        style={{
          flex: "none",
          width: 44,
          font: "600 9px var(--mono)",
          letterSpacing: ".08em",
          color: "var(--link)",
        }}
      >
        RUN
      </span>
      <span style={{ flex: 1, minWidth: 0 }}>
        <Link
          to={`/runs/${enc(repo)}/${run.number}`}
          style={{ color: "var(--fg)" }}
        >
          {run.title}
        </Link>
        <div
          style={{
            font: "11px var(--mono)",
            color: "var(--fg-faint)",
            marginTop: 2,
          }}
        >
          {repo} · #{run.number} · {timeAgo(run.created_at)} ago
        </div>
      </span>
      <StatePill state={run.state} />
    </div>
  );
}

/** One Work Item this person is assigned, in the same grammar as the runs
 * above so the two read as one feed. */
function WorkRow({ item, repo }: { item: WorkItem; repo: string }) {
  return (
    <div
      style={{
        display: "flex",
        alignItems: "baseline",
        gap: 10,
        padding: "8px 14px",
        borderBottom: "1px solid var(--line)",
      }}
    >
      <span
        style={{
          flex: "none",
          width: 44,
          font: "600 9px var(--mono)",
          letterSpacing: ".08em",
          color: "var(--violet)",
        }}
      >
        WORK
      </span>
      <span style={{ flex: 1, minWidth: 0 }}>
        <Link
          to={`/work/${enc(repo)}/${enc(item.key)}`}
          style={{ color: "var(--fg)" }}
        >
          {item.goal}
        </Link>
        <div
          style={{
            font: "11px var(--mono)",
            color: "var(--fg-faint)",
            marginTop: 2,
          }}
        >
          {repo} · {item.key} · {item.type}
        </div>
      </span>
      <StatePill state={item.state} />
    </div>
  );
}
