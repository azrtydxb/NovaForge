import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api, enc } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { Failed, Panel, PanelHead } from "../../components/ui";
import type { GateConfig, Release, Repo } from "../../lib/types";
import { useRefs, useRepoScope } from "./shared";

/** RepoDetails is the code tab's sidebar: what the platform itself knows
 * about this repository record, each line backed by an endpoint. Sections
 * the design draws that no endpoint answers — stars, watchers, languages,
 * per-repo deployments and agent presence — are named once, at the bottom,
 * as not available rather than silently dropped. */
export function RepoDetails() {
  const { org, repo } = useRepoScope();
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}`;
  const w = useWorkspace();
  // The scope's own record is the authority; the workspace list is only the
  // fallback for naming forks, and a URL can name a repository the list has
  // not heard of.
  const record = useRepoRecord();

  const { branches, tags } = useRefs(org, repo);
  const releases = useQuery({
    queryKey: ["releases", org, repo],
    queryFn: () => api.get<{ releases: Release[] }>(`${base}/releases`),
  });
  const gates = useQuery({
    queryKey: ["gates", org, repo],
    queryFn: () =>
      api.get<{ ref: string; commit_sha: string; gates: GateConfig[] }>(
        `${base}/gates`,
      ),
  });

  const forks = w.repos.filter((r) => r.parent_repo_id === record?.id);

  return (
    <div style={{ display: "grid", gap: 14 }}>
      <Panel>
        <PanelHead title="ABOUT" />
        <div style={{ padding: 12, display: "grid", gap: 8 }}>
          <Row label="Default branch">
            <span style={{ font: "11px var(--mono)" }}>
              {record?.default_branch ?? "—"}
            </span>
          </Row>
          {record?.archived ? (
            <Row label="State">
              <span style={{ color: "var(--warn)" }}>
                archived — reads only
              </span>
            </Row>
          ) : null}
          {record?.parent_repo_id ? (
            <Row label="Forked from">
              <span style={{ font: "11px var(--mono)" }}>
                {w.repos.find((r) => r.id === record.parent_repo_id)?.name ??
                  "a repository this organization can no longer see"}
              </span>
            </Row>
          ) : null}
          <Row label="Forks of it">
            <span style={{ font: "11px var(--mono)" }}>
              {forks.length === 0
                ? "none in this organization"
                : forks.map((f) => f.name).join(", ")}
            </span>
          </Row>
        </div>
      </Panel>

      <Panel>
        <PanelHead title="BRANCHES & TAGS" />
        <div style={{ padding: 12, display: "grid", gap: 8 }}>
          <Row label="Branches">
            <Link
              to={`/repos/${enc(repo)}/branches`}
              style={{ font: "11px var(--mono)" }}
            >
              {(branches.data?.refs ?? []).length}
            </Link>
          </Row>
          <Row label="Tags">
            <Link
              to={`/repos/${enc(repo)}/branches?tab=tags`}
              style={{ font: "11px var(--mono)" }}
            >
              {(tags.data?.refs ?? []).length}
            </Link>
          </Row>
        </div>
      </Panel>

      <Panel>
        <PanelHead title="RELEASES" />
        <div style={{ padding: 12, display: "grid", gap: 8 }}>
          {releases.isLoading ? null : releases.error ? (
            <Failed error={releases.error} />
          ) : (releases.data?.releases ?? []).length === 0 ? (
            <span
              style={{ font: "12px var(--sans)", color: "var(--fg-muted)" }}
            >
              none yet
            </span>
          ) : (
            <>
              <Row label="Latest">
                <span style={{ font: "11px var(--mono)" }}>
                  {releases.data?.releases[0]?.tag ?? ""}
                </span>
              </Row>
              <Row label="Count">
                <Link
                  to={`/repos/${enc(repo)}/releases`}
                  style={{ font: "11px var(--mono)" }}
                >
                  {(releases.data?.releases ?? []).length} published
                </Link>
              </Row>
            </>
          )}
        </div>
      </Panel>

      <Panel>
        <PanelHead title="RULES & GATES" />
        <div style={{ padding: 12, display: "grid", gap: 8 }}>
          {gates.isLoading ? null : gates.error ? (
            <Failed error={gates.error} />
          ) : (
            <>
              <Row label="Enabled">
                <span style={{ font: "11px var(--mono)" }}>
                  {(gates.data?.gates ?? []).filter((g) => g.enabled).length} of{" "}
                  {(gates.data?.gates ?? []).length}
                </span>
              </Row>
              <Row label="Declared at">
                <span
                  style={{ font: "11px var(--mono)", wordBreak: "break-all" }}
                >
                  {gates.data?.commit_sha
                    ? `${gates.data.ref} · ${gates.data.commit_sha.slice(0, 8)}`
                    : "—"}
                </span>
              </Row>
              <Link
                to={`/repos/${enc(repo)}/settings`}
                style={{ font: "11px var(--sans)" }}
              >
                Manage in settings
              </Link>
            </>
          )}
        </div>
      </Panel>

      <Panel>
        <PanelHead title="NOT IN THIS DEPLOYMENT" />
        <div
          style={{
            padding: "10px 12px 12px",
            font: "12px var(--sans)",
            color: "var(--fg-muted)",
            lineHeight: 1.7,
          }}
        >
          Stars, watchers, languages, per-repo deployments and agent presence
          have no endpoints here, so this sidebar does not show them.
        </div>
      </Panel>
    </div>
  );
}

function Row({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div style={{ display: "flex", alignItems: "baseline", gap: 10 }}>
      <span
        style={{
          font: "600 11px var(--sans)",
          color: "var(--fg-muted)",
          minWidth: 110,
          flexShrink: 0,
        }}
      >
        {label}
      </span>
      <span style={{ font: "12px var(--sans)", minWidth: 0 }}>{children}</span>
    </div>
  );
}

/** useRepoRecord prefers the repository's own record, fetched by RepoPage,
 * and falls back to the workspace list while it loads. */
function useRepoRecord(): Repo | undefined {
  const { repoRecord, repo } = useRepoScope();
  const w = useWorkspace();
  return repoRecord ?? w.repos.find((r) => r.name === repo);
}
