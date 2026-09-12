import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  type ReactNode,
} from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, enc } from "./api";
import type { Org, Repo } from "./types";

/** Workspace is the (organization, repository) scope every screen reads from.
 *
 * The design's context switcher offers "All projects" plus one entry per
 * repository. Organizations are the platform's hard security boundary, so the
 * switcher's list is the current organization's repositories, and "all" means
 * every repository in that organization — never across organizations, which
 * would be a way across the boundary the whole backend is built to hold. */
export interface Workspace {
  orgs: Org[];
  org: string | null;
  repos: Repo[];
  /** repo is null for "All projects". */
  repo: string | null;
  setOrg: (org: string) => void;
  setRepo: (repo: string | null) => void;
  loading: boolean;
}

const Ctx = createContext<Workspace | null>(null);

const ORG_KEY = "novaforge.org";
const REPO_KEY = "novaforge.repo";

function remembered(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function remember(key: string, value: string | null): void {
  try {
    if (value === null) window.localStorage.removeItem(key);
    else window.localStorage.setItem(key, value);
  } catch {
    /* a scope that cannot be remembered still works for this page */
  }
}

export function WorkspaceProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();

  const orgsQ = useQuery({
    queryKey: ["orgs"],
    queryFn: () => api.get<{ orgs: Org[] }>("/api/v1/orgs"),
  });
  const orgs = orgsQ.data?.orgs ?? [];

  // The remembered organization is used only while it is still one the user
  // belongs to: membership can be revoked between visits, and a stale scope
  // would make every screen fail with a denial rather than simply showing the
  // organizations they do have.
  const stored = remembered(ORG_KEY);
  const org =
    orgs.find((o) => o.name === stored)?.name ?? orgs[0]?.name ?? null;

  const reposQ = useQuery({
    queryKey: ["repos", org],
    queryFn: () =>
      api.get<{ repos: Repo[] }>(`/api/v1/orgs/${enc(org!)}/repos`),
    enabled: org !== null,
  });
  const repos = reposQ.data?.repos ?? [];

  const storedRepo = remembered(REPO_KEY);
  const repo = repos.some((r) => r.name === storedRepo) ? storedRepo : null;

  const setOrg = useCallback(
    (next: string) => {
      remember(ORG_KEY, next);
      remember(REPO_KEY, null);
      qc.invalidateQueries();
    },
    [qc],
  );

  const setRepo = useCallback(
    (next: string | null) => {
      remember(REPO_KEY, next);
      qc.invalidateQueries();
    },
    [qc],
  );

  const value = useMemo<Workspace>(
    () => ({
      orgs,
      org,
      repos,
      repo,
      setOrg,
      setRepo,
      loading: orgsQ.isLoading || reposQ.isLoading,
    }),
    [
      orgs,
      org,
      repos,
      repo,
      setOrg,
      setRepo,
      orgsQ.isLoading,
      reposQ.isLoading,
    ],
  );

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useWorkspace(): Workspace {
  const w = useContext(Ctx);
  if (!w) throw new Error("useWorkspace used outside WorkspaceProvider");
  return w;
}

/** scopedRepos is the set of repositories a screen should read, honouring the
 * "All projects" selection. A screen that lists across repositories asks for
 * this rather than deciding for itself what "all" means. */
export function scopedRepos(w: Workspace): Repo[] {
  if (w.repo === null) return w.repos;
  return w.repos.filter((r) => r.name === w.repo);
}
