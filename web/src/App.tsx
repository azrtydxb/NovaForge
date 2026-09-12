import { useState } from "react";
import { Route, Routes, useLocation } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { api, enc, storedToken } from "./lib/api";
import { WorkspaceProvider, useWorkspace } from "./lib/workspace";
import { Rail } from "./components/Rail";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { Login } from "./Login";
import { TopBar } from "./components/TopBar";
import type { Dashboard, User } from "./lib/types";

import { Home } from "./screens/Home";
import { Work } from "./screens/Work";
import { Swarm } from "./screens/Swarm";
import { Repos } from "./screens/Repos";
import { CI } from "./screens/CI";
import { Runs } from "./screens/Runs";
import { RunDetail } from "./screens/RunDetail";
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
  const [token, setToken] = useState<string | null>(storedToken());

  const me = useQuery({
    queryKey: ["me", token],
    queryFn: () => api.get<User>("/api/v1/user"),
    enabled: token !== null,
    retry: false,
  });

  if (token === null || me.isError) {
    return <Login onSignedIn={() => setToken(storedToken())} />;
  }

  return (
    <WorkspaceProvider>
      <Shell user={me.data ?? null} onSignedOut={() => setToken(null)} />
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
  const { org } = useWorkspace();
  const location = useLocation();

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
        <ErrorBoundary where={location.pathname} key={location.pathname}>
          <Routes>
            <Route path="/" element={<Home />} />
            <Route path="/work" element={<Work />} />
            <Route path="/swarm" element={<Swarm />} />
            <Route path="/repos" element={<Repos />} />
            <Route path="/repos/*" element={<Repos />} />
            <Route path="/ci" element={<CI />} />
            <Route path="/runs" element={<Runs />} />
            <Route path="/runs/:repo/:number" element={<RunDetail />} />
            <Route path="/agents" element={<Agents />} />
            <Route path="/exceptions" element={<Exceptions />} />
            <Route path="/maintenance" element={<Maintenance />} />
            <Route path="/knowledge" element={<Knowledge />} />
            <Route path="/graph" element={<Graph />} />
            <Route path="/orgs" element={<Orgs />} />
            <Route path="/secrets" element={<Secrets />} />
            <Route path="/mcp" element={<Mcp />} />
            <Route path="/settings" element={<Settings />} />
            <Route path="/account" element={<Account />} />
          </Routes>
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
