import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Link,
  Route,
  Routes,
  useLocation,
  useNavigate,
  useParams,
} from "react-router-dom";
import { api, enc } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import { Empty, Failed, Loading, Panel, StatePill } from "../../components/ui";
import { Dialog } from "../../components/Dialog";
import type { CIRun, OrgMember, Repo, User } from "../../lib/types";
import { RepoScopeCtx, Tabs, btn, timeAgo } from "./shared";
import { CodeTab } from "./CodeTab";
import { CommitsTab } from "./CommitsTab";
import { BranchesTab } from "./BranchesTab";
import { CompareTab } from "./CompareTab";
import { ReleasesTab } from "./ReleasesTab";
import { SettingsTab } from "./SettingsTab";

const TABS = [
  ["", "Code"],
  ["commits", "Commits"],
  ["branches", "Branches & tags"],
  ["compare", "Compare"],
  ["releases", "Releases"],
  ["settings", "Settings"],
] as const;

/** RepoPage owns everything under /repos/:repo. It resolves the scope once —
 * the repository's own record, and whether this person may administer it,
 * read from the same org membership the backend enforces — and renders the
 * design's repository header with its tabs over the tab components. */
export function RepoPage() {
  const { repo = "" } = useParams();
  const w = useWorkspace();
  const org = w.org ?? "";
  const location = useLocation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [forking, setForking] = useState(false);

  const record = useQuery({
    queryKey: ["repo", org, repo],
    queryFn: () => api.get<Repo>(`/api/v1/orgs/${enc(org)}/repos/${enc(repo)}`),
    enabled: org !== "" && repo !== "",
  });

  // Whether to offer administration is decided from the platform's own record
  // of this person's role, the same role git-platform enforces. Offering the
  // controls to a member would only lead them to a refusal.
  const me = useQuery({
    queryKey: ["user"],
    queryFn: () => api.get<User>("/api/v1/user"),
  });
  const members = useQuery({
    queryKey: ["members", org],
    queryFn: () =>
      api.get<{ members: OrgMember[] }>(`/api/v1/orgs/${enc(org)}/members`),
    enabled: org !== "",
  });
  const myRole = members.data?.members.find(
    (m) => m.user_id === me.data?.id,
  )?.role;
  const canAdminister = myRole === "owner" || myRole === "admin";

  const fork = useMutation({
    mutationFn: (name: string) =>
      api.post<Repo>(`/api/v1/orgs/${enc(org)}/repos/${enc(repo)}/forks`, {
        name,
      }),
    onSuccess: () => {
      setForking(false);
      void qc.invalidateQueries({ queryKey: ["repos", org] });
    },
  });

  // The latest CI run on the default branch, for the header's status chip.
  // The design draws a CI chip in the repository header and CI runs are
  // served per repository, so this is the platform's answer, not a guess.
  const ci = useQuery({
    queryKey: ["ci", org, repo],
    queryFn: () =>
      api.get<{ runs: CIRun[] }>(
        `/api/v1/orgs/${enc(org)}/repos/${enc(repo)}/ci/runs`,
      ),
    enabled: record.isSuccess,
    staleTime: 30_000,
  });
  const defaultBranch = record.data?.default_branch ?? "main";
  const ciOnDefault = (ci.data?.runs ?? [])
    .filter((r) => r.ref === defaultBranch)
    .sort((a, b) => (a.created_at < b.created_at ? 1 : -1))[0];

  if (org === "" || repo === "") {
    return (
      <Panel>
        <Empty>No repository is open.</Empty>
      </Panel>
    );
  }

  // The workspace's own list is not the authority on whether a repository
  // exists — a URL can name one it has not heard of — so a 404 here is
  // rendered as the answer it is.
  if (record.error) {
    return (
      <Panel>
        <Failed error={record.error} />
        <div style={{ padding: "0 16px 16px" }}>
          <Link to="/repos" style={{ font: "12px var(--sans)" }}>
            Back to repositories
          </Link>
        </div>
      </Panel>
    );
  }

  // The tab is the first path segment after /repos/:repo; "" is Code.
  const rest = location.pathname.replace(new RegExp(`^/repos/[^/]+/?`), "");
  const activeTab = (rest.split("/")[0] ?? "").split("?")[0] ?? "";

  return (
    <RepoScopeCtx.Provider
      value={{ org, repo, repoRecord: record.data, canAdminister }}
    >
      <div style={{ flex: 1, overflowY: "auto", padding: "22px 26px" }}>
        <header style={{ marginBottom: 12 }}>
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: 10,
              flexWrap: "wrap",
            }}
          >
            <span
              style={{
                font: "600 11px var(--sans)",
                letterSpacing: ".08em",
                color: "var(--fg-faint)",
              }}
            >
              {org.toUpperCase()} · ORGANIZATION
            </span>
            <div style={{ flex: 1 }} />
            <button style={btn} onClick={() => setForking(true)}>
              Fork
              {w.repos.filter((r) => r.parent_repo_id === record.data?.id)
                .length > 0
                ? ` ${w.repos.filter((r) => r.parent_repo_id === record.data?.id).length}`
                : ""}
            </button>
          </div>
          <h1 style={{ font: "600 19px var(--sans)", margin: "4px 0 2px" }}>
            <Link to="/repos" style={{ color: "var(--fg)" }}>
              {org}
            </Link>
            <span style={{ color: "var(--fg-faint)" }}> / </span>
            {repo}
            {record.data?.archived ? (
              <span
                style={{
                  marginLeft: 10,
                  background: "var(--warn-bg)",
                  color: "var(--warn)",
                  borderRadius: 99,
                  padding: "2px 9px",
                  font: "600 10px var(--mono)",
                  verticalAlign: "middle",
                }}
              >
                archived
              </span>
            ) : null}
            {record.data?.parent_repo_id ? (
              <span
                style={{
                  marginLeft: 10,
                  background: "var(--accent-soft)",
                  color: "var(--fg-muted)",
                  borderRadius: 99,
                  padding: "2px 9px",
                  font: "600 10px var(--mono)",
                  verticalAlign: "middle",
                }}
              >
                fork
              </span>
            ) : null}
          </h1>
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: 12,
              flexWrap: "wrap",
            }}
          >
            <span
              style={{ font: "11px var(--mono)", color: "var(--fg-muted)" }}
            >
              default {defaultBranch}
            </span>
            {ciOnDefault ? (
              <span
                style={{ display: "inline-flex", alignItems: "center", gap: 6 }}
              >
                <StatePill state={ciOnDefault.status} />
                <span
                  style={{ font: "11px var(--mono)", color: "var(--fg-faint)" }}
                >
                  ci on {ciOnDefault.ref} · {ciOnDefault.commit_sha.slice(0, 8)}{" "}
                  · {timeAgo(ciOnDefault.created_at)}
                </span>
              </span>
            ) : ci.isSuccess ? (
              <span
                style={{ font: "11px var(--mono)", color: "var(--fg-faint)" }}
              >
                no CI runs yet
              </span>
            ) : null}
          </div>
        </header>

        <nav
          aria-label="Repository"
          style={{
            borderBottom: "1px solid var(--line)",
            marginBottom: 16,
          }}
        >
          <Tabs
            tabs={TABS.map(([k, label]) => ({ key: k, label }))}
            active={activeTab}
            onPick={(k) =>
              navigate(k === "" ? `/repos/${repo}` : `/repos/${repo}/${k}`)
            }
          />
        </nav>

        {record.isLoading ? <Loading /> : null}
        {record.isSuccess ? (
          <Routes>
            <Route index element={<CodeTab />} />
            <Route path="code" element={<CodeTab />} />
            <Route path="commits" element={<CommitsTab />} />
            <Route path="branches" element={<BranchesTab />} />
            <Route path="compare" element={<CompareTab />} />
            <Route path="releases" element={<ReleasesTab />} />
            <Route path="settings" element={<SettingsTab />} />
            <Route path="*" element={<Empty>Nothing is at this path.</Empty>} />
          </Routes>
        ) : null}
      </div>

      {forking ? (
        <Dialog
          title="Fork repository"
          description="Forks carry the whole history into a new repository in this organization. Nothing is shared after the fork: the fork's history is its own from then on."
          submitLabel="Fork"
          busy={fork.isPending}
          error={fork.error}
          fields={[
            {
              name: "name",
              label: "Name",
              required: true,
              placeholder: `${repo}-fork`,
            },
          ]}
          onSubmit={(v) => fork.mutate(v.name ?? "")}
          onClose={() => setForking(false)}
        />
      ) : null}
    </RepoScopeCtx.Provider>
  );
}
