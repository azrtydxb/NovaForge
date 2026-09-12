import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App } from "./App";
import "./theme.css";

const qc = new QueryClient({
  defaultOptions: {
    queries: {
      // The platform is live: runs change state, CI streams, agents finish.
      // A stale-forever cache would show a queued run long after it ended.
      staleTime: 5_000,
      refetchInterval: 15_000,
      retry: (count, error) =>
        // Re-requesting a 401 or a 404 only repeats the same answer.
        count < 2 &&
        !(
          typeof error === "object" &&
          error !== null &&
          "status" in error &&
          [401, 403, 404, 501].includes((error as { status: number }).status)
        ),
    },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={qc}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
);
