import { useEffect, useRef, useState } from "react";
import { Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, api, enc, storedToken, setStoredToken } from "./lib/api";
import { WorkspaceProvider, useWorkspace } from "./lib/workspace";
import { Rail } from "./components/Rail";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { Empty, Failed, Loading, Page } from "./components/ui";
import { Login } from "./Login";
import { TopBar } from "./components/TopBar";
import type { Dashboard, User } from "./lib/types";

import { Home } from "./screens/Home";
import { Work } from "./screens/Work";
import { WorkItemDetail } from "./screens/WorkItem";
import { Swarm } from "./screens/Swarm";
import { Repos } from "./screens/Repos";
import { CI } from "./screens/CI";
import { Runs } from "./screens/Runs";
import { RunDetail } from "./screens/RunDetail";
import { AgentRun } from "./screens/AgentRun";
import { Agents } from "./screens/Agents";
import { Exceptions } from "./screens/Exceptions";
import { Maintenance } from "./screens/Maintenance";
import { Knowledge } from "./screens/Knowledge";
import { Graph } from "./screens/Graph";
import { Orgs } from "./screens/Orgs";
import { Secrets } from "./screens/Secrets";
import { Mcp } from "./screens/Mcp";
import { Settings } from "./screens/Settings";
import { Account } from "./screens/Account";

export function App() {
  const qc = useQueryClient();
  const [token, setToken] = useState<string | null>(storedToken());

  const me = useQuery({
    queryKey: ["me", token],
    queryFn: () => api.get<User>("/api/v1/user"),
    enabled: token !== null,
    retry: false,
  });

  useEffect(() => {
    if (me.error instanceof ApiError && me.error.status === 401) {
      setStoredToken(null);
      qc.clear();
      setToken(null);
    }
  }, [me.error, qc]);

  if (token === null) {
    return (
      <Login
        onSignedIn={() => {
          qc.clear();
          setToken(storedToken());
        }}
      />
    );
  }
  if (me.isLoading) return <Loading />;
  if (me.error)
    return (
      <Page title="Session">
        <Failed error={me.error} />
        <button onClick={() => void me.refetch()}>Retry</button>
      </Page>
    );

  return (
    <WorkspaceProvider>
      <Shell
        user={me.data ?? null}
        onSignedOut={() => {
          qc.clear();
          setToken(null);
        }}
      />
    </WorkspaceProvider>
  );
}

function Shell({
  user,
  onSignedOut,
}: {
  user: User | null;
  onSignedOut: () => void;
}) {
  const w = useWorkspace();
  const { org } = w;
  const location = useLocation();
  const navigate = useNavigate();
  const previousOrg = useRef(org);
  useEffect(() => {
    // Detail URLs are relative to the organization. Never reinterpret the old
    // run number or Work Item key in a newly selected organization.
    if (previousOrg.current !== null && previousOrg.current !== org)
      navigate("/");
    previousOrg.current = org;
  }, [org, navigate]);

  // The exception count on the rail is the dashboard's own tally of what needs
  // a person, so the badge and the Exceptions screen cannot disagree.
  const dash = useQuery({
    queryKey: ["dashboard", org],
    queryFn: () => api.get<Dashboard>(`/api/v1/orgs/${enc(org!)}/dashboard`),
    enabled: org !== null,
  });

  return (
    <div
      style={{
        display: "flex",
        height: "100vh",
        background: "var(--bg)",
        color: "var(--fg)",
        overflow: "hidden",
      }}
    >
      <Rail exceptionCount={exceptionTotal(dash.data)} />
      <div
        style={{
          flex: 1,
          display: "flex",
          flexDirection: "column",
          minWidth: 0,
        }}
      >
        <TopBar user={user} onSignedOut={onSignedOut} />
        {/* Keyed on the path so moving to another screen clears a previous
            screen's failure rather than carrying it along. */}
        <ErrorBoundary
          where={location.pathname}
          key={`${org}/${w.repo}/${location.pathname}`}
        >
          {w.loading ? (
            <Loading />
          ) : w.error ? (
            <Page title="Workspace">
              <Failed error={w.error} />
              <button onClick={w.retry}>Retry</button>
            </Page>
          ) : (
            <Routes>
              <Route path="/" element={<Home />} />
              <Route path="/work" element={<Work />} />
              <Route path="/work/:repo/:key" element={<WorkItemDetail />} />
              <Route path="/swarm" element={<Swarm />} />
              <Route path="/repos" element={<Repos />} />
              <Route path="/repos/*" element={<Repos />} />
              <Route path="/ci" element={<CI />} />
              <Route path="/runs" element={<Runs />} />
              <Route path="/runs/:repo/:number" element={<RunDetail />} />
              <Route path="/agents" element={<Agents />} />
              <Route path="/agent-runs/:id" element={<AgentRun />} />
              <Route path="/exceptions" element={<Exceptions />} />
              <Route path="/maintenance" element={<Maintenance />} />
              <Route path="/knowledge" element={<Knowledge />} />
              <Route path="/graph" element={<Graph />} />
              <Route path="/orgs" element={<Orgs />} />
              <Route path="/secrets" element={<Secrets />} />
              <Route path="/mcp" element={<Mcp />} />
              <Route path="/settings" element={<Settings />} />
              <Route path="/account" element={<Account />} />
              <Route
                path="*"
                element={
                  <Page title="Page not found">
                    <Empty>This page does not exist.</Empty>
                  </Page>
                }
              />
            </Routes>
          )}
        </ErrorBoundary>
      </div>
    </div>
  );
}

/** exceptionTotal is the number the rail badges. It is the length of the
 * platform's own exception list rather than a sum of the counts beside it:
 * summing would double-count an item that is both blocked and failing a gate,
 * and the badge would then disagree with the list it points at. */
export function exceptionTotal(d: Dashboard | undefined): number | null {
  if (!d) return null;
  return d.exceptions?.length ?? 0;
}
