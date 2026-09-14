import { useState } from "react";
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
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
import { Row } from "./Home";
import { Dialog, NewButton } from "../components/Dialog";
import type { EngineeringRun, Ref, Repo } from "../lib/types";

/** Runs lists Engineering Runs — the platform's pull request, which carries
 * plan and proof rather than only a diff. */
export function Runs() {
  const w = useWorkspace();
  const repos = scopedRepos(w);
  // Opening a run is two questions when the scope is every repository: which
  // repository first, because the branches offered depend on it.
  const [choosingRepo, setChoosingRepo] = useState(false);
  const [openingIn, setOpeningIn] = useState<Repo | null>(null);

  const queries = useQueries({
    queries: repos.map((r) => ({
      queryKey: ["runs", w.org, r.name],
      queryFn: () =>
        api.get<{ runs: EngineeringRun[] }>(
          `/api/v1/orgs/${enc(w.org!)}/repos/${enc(r.name)}/runs`,
        ),
      enabled: w.org !== null,
    })),
  });

  const loading = queries.some((q) => q.isLoading);
  const rows = queries
    .flatMap((q, i) =>
      (q.data?.runs ?? []).map((run) => ({ run, repo: repos[i]!.name })),
    )
    .sort((a, b) => b.run.created_at.localeCompare(a.run.created_at));

  return (
    <Page
      title="Engineering Runs"
      subtitle="Every run presents its plan, its change impact, and its per-gate proof"
      actions={
        repos.length > 0 ? (
          <NewButton
            label="New run"
            onClick={() => {
              if (repos.length === 1) setOpeningIn(repos[0]!);
              else setChoosingRepo(true);
            }}
          />
        ) : null
      }
    >
      {choosingRepo ? (
        <Dialog
          title="New Engineering Run"
          submitLabel="Next"
          fields={[
            {
              name: "repo",
              label: "Repository",
              type: "select",
              options: repos.map((r) => r.name),
              required: true,
            },
          ]}
          busy={false}
          error={null}
          onSubmit={(v) => {
            setChoosingRepo(false);
            setOpeningIn(repos.find((r) => r.name === v.repo) ?? null);
          }}
          onClose={() => setChoosingRepo(false)}
        />
      ) : null}
      {openingIn && w.org ? (
        <NewRun
          key={openingIn.name}
          org={w.org}
          repo={openingIn}
          onClose={() => setOpeningIn(null)}
        />
      ) : null}

      <Panel>
        <PanelHead>
          <span style={{ width: 60 }}>RUN</span>
          <span style={{ flex: 1 }}>TITLE</span>
          <span style={{ width: 170 }}>BRANCH</span>
          <span style={{ width: 130 }}>AUTHOR</span>
          <span style={{ width: 110 }}>REPOSITORY</span>
          <span style={{ width: 80 }}>STATE</span>
        </PanelHead>
        {loading ? (
          <Loading />
        ) : rows.length === 0 ? (
          <Empty>
            No Engineering Runs in this scope.
            <br />
            An agent opens one when it finishes work; a person opens one with{" "}
            New run.
          </Empty>
        ) : (
          rows.map(({ run, repo }) => (
            <Row key={run.id}>
              <Link
                to={`/runs/${enc(repo)}/${run.number}`}
                style={{ width: 60, font: "600 12px var(--mono)" }}
              >
                #{run.number}
              </Link>
              <span style={{ flex: 1, font: "13px var(--sans)" }}>
                {run.title}
              </span>
              <span
                style={{
                  width: 170,
                  font: "11px var(--mono)",
                  color: "var(--fg-dim)",
                  overflow: "hidden",
                  textOverflow: "ellipsis",
                }}
              >
                {run.source_ref} → {run.target_ref}
              </span>
              <span
                style={{
                  width: 130,
                  font: "11px var(--mono)",
                  color: "var(--fg-muted)",
                }}
              >
                {run.author_kind === "agent"
                  ? run.agent_name || "agent"
                  : "human"}
              </span>
              <span
                style={{
                  width: 110,
                  font: "11px var(--mono)",
                  color: "var(--fg-faint)",
                }}
              >
                {repo}
              </span>
              <span style={{ width: 80 }}>
                <StatePill state={run.state} />
              </span>
            </Row>
          ))
        )}
      </Panel>
    </Page>
  );
}

/** NewRun opens an Engineering Run from one of the repository's branches. The
 * branches are the repository's own list, so a run cannot be opened from a
 * branch that does not exist; the edge checks the same thing again. */
function NewRun({
  org,
  repo,
  onClose,
}: {
  org: string;
  repo: Repo;
  onClose: () => void;
}) {
  const base = `/api/v1/orgs/${enc(org)}/repos/${enc(repo.name)}`;
  const qc = useQueryClient();
  const navigate = useNavigate();

  const branches = useQuery({
    queryKey: ["branches", org, repo.name],
    queryFn: () => api.get<{ refs: Ref[] }>(`${base}/branches`),
  });

  const create = useMutation({
    mutationFn: (v: Record<string, string>) =>
      api.post<EngineeringRun>(`${base}/runs`, {
        title: v.title,
        source_ref: v.source,
        target_ref: v.target,
        work_item: v.work_item?.trim() || undefined,
      }),
    onSuccess: (run) => {
      qc.invalidateQueries({ queryKey: ["runs"] });
      onClose();
      navigate(`/runs/${enc(repo.name)}/${run.number}`);
    },
  });

  if (branches.isLoading) return null;
  if (branches.error) {
    return (
      <div
        onClick={onClose}
        style={{
          position: "fixed",
          inset: 0,
          background: "rgba(0,0,0,.55)",
          display: "grid",
          placeItems: "center",
          zIndex: 100,
        }}
      >
        <div style={{ width: 420, maxWidth: "calc(100vw - 32px)" }}>
          <Failed error={branches.error} />
        </div>
      </div>
    );
  }

  const names = (branches.data?.refs ?? []).map((r) => r.name);
  // A run merges a branch into another, so the default branch is where it
  // goes unless someone says otherwise, and is not offered as a source. It
  // is listed as a target even before it has a commit: a new repository's
  // default branch is still the right place to merge into.
  const sources = names.filter((n) => n !== repo.default_branch);
  const targets = [
    repo.default_branch,
    ...names.filter((n) => n !== repo.default_branch),
  ];

  return (
    <Dialog
      title={`New Engineering Run in ${repo.name}`}
      submitLabel="Open run"
      fields={[
        {
          name: "title",
          label: "Title",
          required: true,
          placeholder: "What this change does",
        },
        {
          name: "source",
          label: "Source branch",
          type: "select",
          options: sources,
          required: true,
          help:
            sources.length === 0
              ? `This repository has no branch other than ${repo.default_branch} to open a run from.`
              : "The branch whose change the run carries.",
        },
        {
          name: "target",
          label: "Target branch",
          type: "select",
          options: targets,
          required: true,
        },
        {
          name: "work_item",
          label: "Work Item",
          placeholder: "NF-12",
          help: "The Work Item this change is for, in this repository.",
        },
      ]}
      busy={create.isPending}
      error={create.error}
      onSubmit={(v) => create.mutate(v)}
      onClose={onClose}
    />
  );
}
