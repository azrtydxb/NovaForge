// Runs in the supported ego-browser against Vite. These are rendered UI
// regressions with controlled HTTP responses, not datastore acceptance tests.
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { WorkItemDetail } from "../src/screens/WorkItem";
import { RunReviews } from "../src/components/RunReviews";
import { RunDetail } from "../src/screens/RunDetail";
import { Route, Routes } from "react-router-dom";
import { App } from "../src/App";
import { api, ApiError, setStoredToken, storedToken } from "../src/lib/api";
import { WorkspaceProvider, useWorkspace } from "../src/lib/workspace";
import { Dialog, Confirm } from "../src/components/Dialog";
import { Home } from "../src/screens/Home";
import { Work } from "../src/screens/Work";
import { Runs } from "../src/screens/Runs";
import { CI } from "../src/screens/CI";
import { Swarm } from "../src/screens/Swarm";
import { Repos } from "../src/screens/Repos";
import { Orgs } from "../src/screens/Orgs";
import { Agents } from "../src/screens/Agents";
import { Account } from "../src/screens/Account";
import { Secrets } from "../src/screens/Secrets";
import { Knowledge } from "../src/screens/Knowledge";
import { Settings } from "../src/screens/Settings";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
const host = document.getElementById("tests")!;
let root: Root;
let client: QueryClient;
let requests: string[];
type Handler = (
  path: string,
  init?: RequestInit,
) => unknown | Response | Promise<unknown>;
const user = { id: "person", username: "tester", email: "t@example.test" };
const repos = ["alpha", "beta"].map((name) => ({
  id: name,
  name,
  org_id: "one",
  default_branch: "main",
}));
const defaults: Handler = (path) => {
  if (path === "/api/v1/user") return user;
  if (path === "/api/v1/orgs")
    return {
      orgs: [
        { id: "one", name: "one" },
        { id: "two", name: "two" },
      ],
    };
  if (path.endsWith("/repos")) return { repos };
  if (path.endsWith("/members"))
    return {
      members: [{ user_id: "person", username: "tester", role: "owner" }],
    };
  if (path.endsWith("/agents")) return { agents: [] };
  if (path.endsWith("/dashboard"))
    return {
      exceptions: [],
      agents_running: 0,
      agents_blocked: 0,
      need_human_review: 0,
      architecture_decisions: 0,
      gate_failures: 0,
      ready_to_auto_merge: 0,
    };
  if (path.endsWith("/branches"))
    return { refs: [{ name: "main", kind: "branch", sha: "a" }] };
  if (path.endsWith("/tags")) return { refs: [] };
  if (path.includes("/commits/")) return { commits: [] };
  if (path.includes("/tree/")) return { entries: [] };
  if (path.endsWith("/work")) return { items: [] };
  if (path.endsWith("/runs")) return { runs: [] };
  if (path.endsWith("/subtasks")) return { subtasks: [] };
  if (path.endsWith("/approvals/policy")) return { rules: [] };
  throw new Error(`Unexpected test request: ${path}`);
};
const failure = () =>
  new Response(JSON.stringify({ error: "upstream exploded" }), { status: 503 });
function assert(ok: unknown, message: string): asserts ok {
  if (!ok) throw new Error(message);
}
function button(label: string) {
  const el = [...host.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === label,
  );
  assert(el, `Button not found: ${label}`);
  return el;
}
async function flush() {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 25));
  });
}
async function click(el: HTMLElement) {
  await act(async () => el.click());
  await flush();
}
async function mount(
  node: ReactNode,
  handler: Handler = defaults,
  path = "/",
  workspace = true,
) {
  requests = [];
  window.fetch = async (input, init) => {
    const path = String(input);
    requests.push(`${init?.method ?? "GET"} ${path}`);
    const result = await handler(path, init);
    return result instanceof Response
      ? result
      : new Response(JSON.stringify(result), {
          headers: { "Content-Type": "application/json" },
        });
  };
  client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: Infinity },
      mutations: { retry: false },
    },
  });
  root = createRoot(host);
  await act(async () =>
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[path]}>
          {workspace ? <WorkspaceProvider>{node}</WorkspaceProvider> : node}
        </MemoryRouter>
      </QueryClientProvider>,
    ),
  );
  await flush();
  await flush();
}
function ScopeProbe() {
  const w = useWorkspace();
  return (
    <>
      <output>
        {w.org}/{w.repo ?? "all"}
      </output>
      <button onClick={() => w.setRepo("beta")}>select beta</button>
      <button onClick={() => w.setOrg("two")}>select two</button>
    </>
  );
}
const workItem = {
  id: "item",
  key: "NF-1",
  repo_id: "alpha",
  type: "feature",
  goal: "Original goal",
  acceptance: ["preserved"],
  constraints: [],
  required_gates: ["tests"],
  state: "open",
  assignee_kind: "",
  assignee_id: "",
  maintenance_proposal: false,
  execution_claimed: false,
};
const detailDefaults: Handler = (p, init) => {
  if (p.endsWith("/work/NF-1")) return workItem;
  if (p.endsWith("/comments")) return { comments: [] };
  if (p.endsWith("/agent-runs")) return { runs: [] };
  return defaults(p, init);
};
const cases: [string, () => Promise<void>][] = [
  [
    "Unknown review currency retains durable history without stale/current labels",
    async () => {
      await mount(
        <RunReviews
          base="/old"
          org="one"
          repo="alpha"
          run={
            {
              id: "run",
              number: 1,
              title: "review",
              state: "open",
              source_ref: "feature",
              target_ref: "main",
            } as never
          }
        />,
        (p) =>
          p.endsWith("/reviews")
            ? {
                current_source_sha: "",
                current_source_available: false,
                reviews: [
                  {
                    reviewer_id: "reviewer",
                    reviewer_kind: "user",
                    verdict: "approve",
                    summary: "durable evidence",
                    source_sha: "inspected-sha",
                    created_at: "then",
                  },
                ],
              }
            : defaults(p),
      );
      assert(
        host.textContent?.includes("durable evidence") &&
          host.textContent?.includes("currency unknown"),
        "unknown currency not explained with durable evidence",
      );
      assert(
        !host.textContent?.includes("stale source") &&
          ![...host.querySelectorAll("button")].some(
            (b) => b.textContent === "Review",
          ),
        "unknown labeled stale or review enabled",
      );
      assert(
        requests.some((p) => p.includes("/engineering-runs/run/reviews")),
        "history still depends on Git slug lookup",
      );
    },
  ],
  [
    "Blocked work cannot offer execution and historical admission hides intent mutations",
    async () => {
      for (const item of [
        { ...workItem, state: "blocked" },
        { ...workItem, execution_claimed: true },
      ]) {
        await mount(
          <Routes>
            <Route path="/work/:repo/:key" element={<WorkItemDetail />} />
          </Routes>,
          (p) =>
            p.endsWith("/work/NF-1")
              ? item
              : p.endsWith("/agents")
                ? {
                    agents: [
                      {
                        id: "agent",
                        name: "builder",
                        enabled: true,
                        role: "builder",
                      },
                    ],
                  }
                : detailDefaults(p),
          "/work/alpha/NF-1",
        );
        if (item.state === "blocked")
          assert(
            !host.textContent?.includes("Start an Agent Run"),
            "blocked item offers execution",
          );
        if (item.execution_claimed)
          assert(
            !host.textContent?.includes("Edit intent") &&
              !host.textContent?.includes("Block work"),
            "started intent offers unstarted edits",
          );
        await act(async () => root.unmount());
        root = undefined!;
        client.clear();
        host.replaceChildren();
      }
    },
  ],
  [
    "Agents do not call failed statistics unavailable",
    async () => {
      await mount(<Agents />, (p) =>
        p.endsWith("/agents/stats")
          ? failure()
          : p.endsWith("/agents")
            ? {
                agents: [
                  {
                    id: "agent",
                    name: "builder",
                    role: "implementer",
                    enabled: true,
                  },
                ],
              }
            : defaults(p),
      );
      assert(
        host.textContent?.includes("Request failed (503)"),
        "statistics failure masked as unavailable",
      );
    },
  ],
  [
    "CI refetches the final log when its job becomes terminal",
    async () => {
      const ciRun = {
        id: "ci",
        ref: "main",
        commit_sha: "aaa",
        created_at: "2026-01-01",
        status: "running",
      };
      const job = { id: "job", name: "test", status: "running", detail: "" };
      let reads = 0;
      await mount(<CI />, (p) => {
        if (p.endsWith("/ci/runs")) return { runs: [ciRun] };
        if (p.endsWith("/runs/ci")) return { run: ciRun, jobs: [job] };
        if (p.endsWith("/logs")) {
          reads++;
          return { lines: reads === 1 ? ["live"] : ["live", "final"] };
        }
        if (p.endsWith("/artifacts")) return { artifacts: [] };
        return defaults(p);
      });
      await act(async () => {
        client.setQueryData(["ci-run", "one", "alpha", "ci"], {
          run: { ...ciRun, status: "success" },
          jobs: [{ ...job, status: "success" }],
        });
      });
      await flush();
      await flush();
      assert(
        host.textContent?.includes("final"),
        "terminal job stopped polling before final log read",
      );
    },
  ],

  [
    "Agent SSE uses header authentication and parses split frames",
    async () => {
      let auth = "";
      const chunks = [
        "event: message\r\nda",
        'ta: {"run_id":"run","at":"now"}\r\n\r',
        "\n",
      ];
      await mount(<div />, (_p, init) => {
        auth = new Headers(init?.headers).get("Authorization") ?? "";
        return new Response(
          new ReadableStream({
            start(c) {
              for (const s of chunks) c.enqueue(new TextEncoder().encode(s));
              c.close();
            },
          }),
          { headers: { "Content-Type": "text/event-stream" } },
        );
      });
      const events: unknown[] = [];
      await api.events(
        "/api/v1/orgs/one/agent-runs/run/events",
        new AbortController().signal,
        (event, data) => {
          if (event === "message") events.push(data);
        },
      );
      assert(
        auth === "Bearer test-session" &&
          events.length === 1 &&
          JSON.stringify(events).includes("run_id"),
        "SSE lost authentication or chunked frame",
      );
      assert(
        !requests.some((r) => r.includes("test-session")),
        "credential leaked into URL",
      );
    },
  ],
  [
    "Work Item edit sends explicit fields and prior intent",
    async () => {
      const captured: { payload?: Record<string, unknown> } = {};
      await mount(
        <Routes>
          <Route path="/work/:repo/:key" element={<WorkItemDetail />} />
        </Routes>,
        (p, init) => {
          if (init?.method === "PATCH") {
            captured.payload = JSON.parse(String(init.body));
            return workItem;
          }
          return detailDefaults(p, init);
        },
        "/work/alpha/NF-1",
      );
      await click(button("Edit intent"));
      assert(
        host.querySelector("input")?.value === "Original goal",
        "editor lost initial intent",
      );
      await click(button("Save intent"));
      assert(
        captured.payload &&
          "expected" in captured.payload &&
          JSON.stringify(captured.payload.expected).includes("Original goal"),
        "PATCH omitted optimistic expected intent",
      );
    },
  ],
  [
    "Work Item human transition sends expected state",
    async () => {
      const captured: { payload?: Record<string, unknown> } = {};
      await mount(
        <Routes>
          <Route path="/work/:repo/:key" element={<WorkItemDetail />} />
        </Routes>,
        (p, init) => {
          if (p.endsWith("/transitions")) {
            captured.payload = JSON.parse(String(init?.body));
            return { ...workItem, state: "blocked" };
          }
          return detailDefaults(p, init);
        },
        "/work/alpha/NF-1",
      );
      await click(button("Block work"));
      assert(
        captured.payload &&
          captured.payload.expected_state === "open" &&
          captured.payload.to_state === "blocked",
        "transition omitted state comparison",
      );
    },
  ],
  [
    "Run review presents immutable diff and sends inspected SHA",
    async () => {
      const captured: { payload?: Record<string, unknown> } = {};
      await mount(
        <Routes>
          <Route path="/runs/:repo/:number" element={<RunDetail />} />
        </Routes>,
        (p, init) => {
          if (init?.method === "POST") {
            captured.payload = JSON.parse(String(init.body));
            return {};
          }
          if (p.endsWith("/runs/1"))
            return {
              id: "run",
              number: 1,
              title: "change",
              state: "open",
              source_ref: "feature",
              target_ref: "main",
              author_kind: "user",
            };
          if (p.endsWith("/reviews"))
            return {
              reviews: [],
              current_source_sha: "inspected-sha",
              current_source_available: true,
            };
          if (p.endsWith("/proof")) return { proof: [] };
          if (p.endsWith("/approvals"))
            return { approvals: [], can_decide: false };
          if (p.includes("/diff?"))
            return { unified: "+exact inspected change" };
          return defaults(p, init);
        },
        "/runs/alpha/1",
      );
      await click(button("Review"));
      assert(
        host.textContent?.includes("+exact inspected change"),
        "review offers verdict without code",
      );
      assert(
        requests.some((r) => r.includes("to=inspected-sha")),
        "review diff uses moving branch",
      );
      await click(button("Submit"));
      assert(
        captured.payload &&
          captured.payload.expected_source_sha === "inspected-sha",
        "review omitted inspected SHA",
      );
    },
  ],
  [
    "Workspace changes immediately when persistence is blocked",
    async () => {
      await mount(<ScopeProbe />);
      const original = Storage.prototype.setItem;
      Storage.prototype.setItem = () => {
        throw new Error("blocked");
      };
      try {
        await click(button("select beta"));
        assert(
          host.textContent?.includes("one/beta"),
          "scope still depends on localStorage",
        );
      } finally {
        Storage.prototype.setItem = original;
      }
    },
  ],
  [
    "Token works in memory when persistence is blocked",
    async () => {
      const original = Storage.prototype.setItem;
      Storage.prototype.setItem = () => {
        throw new Error("blocked");
      };
      try {
        setStoredToken("ephemeral-test-token");
        assert(
          storedToken() === "ephemeral-test-token",
          "page lost its credential",
        );
      } finally {
        Storage.prototype.setItem = original;
        setStoredToken(null);
      }
    },
  ],
  [
    "Workspace reports repository read failure rather than empty scope",
    async () => {
      await mount(
        <App />,
        (p) => (p.endsWith("/repos") ? failure() : defaults(p)),
        "/work",
        false,
      );
      assert(
        host.textContent?.includes("Request failed (503)"),
        "repository failure hidden",
      );
      assert(
        !host.textContent?.includes("No Work Items"),
        "failed scope shown as empty",
      );
    },
  ],
  [
    "404 is missing resource, not unavailable deployment",
    async () => {
      assert(
        !new ApiError(404, "not found").unavailable,
        "404 masks missing or denied resource",
      );
      assert(
        new ApiError(501, "not configured").unavailable,
        "501 unavailable semantics lost",
      );
    },
  ],
  [
    "Malformed successful API response fails explicitly",
    async () => {
      await mount(<div />, () => new Response("<html>proxy error</html>"));
      let rejected = false;
      try {
        await api.get("/bad");
      } catch {
        rejected = true;
      }
      assert(rejected, "invalid JSON silently returned as null");
    },
  ],
  ...(
    [
      ["Home", Home],
      ["Work", Work],
      ["Runs", Runs],
      ["CI", CI],
      ["Swarm", Swarm],
    ] as const
  ).map(([name, Screen]): [string, () => Promise<void>] => [
    `${name} reports failed lists`,
    async () => {
      await mount(<Screen />, (p) =>
        /\/(work|runs)$/.test(p) ? failure() : defaults(p),
      );
      assert(
        host.textContent?.includes("Request failed (503)"),
        `${name} silently renders failed lists as empty`,
      );
    },
  ]),
  [
    "Create dialog is modal and enforces required fields",
    async () => {
      let submitted = 0;
      await mount(
        <Dialog
          title="Create"
          submitLabel="Save"
          fields={[{ name: "goal", label: "Goal", required: true }]}
          busy={false}
          error={null}
          onSubmit={() => submitted++}
          onClose={() => {}}
        />,
        defaults,
        "/",
        false,
      );
      assert(
        host.querySelector('[role="dialog"],dialog[open]'),
        "no dialog semantics",
      );
      assert(
        host.querySelector("input")?.required,
        "required input is optional",
      );
      await click(button("Save"));
      assert(submitted === 0, "empty required field submitted");
      assert(
        host.contains(document.activeElement),
        "focus not moved into modal",
      );
    },
  ],
  [
    "Busy dialogs cannot be dismissed or resubmitted",
    async () => {
      let closed = 0,
        submitted = 0;
      await mount(
        <Dialog
          title="Create"
          submitLabel="Save"
          fields={[]}
          busy
          error={null}
          onSubmit={() => submitted++}
          onClose={() => closed++}
        />,
        defaults,
        "/",
        false,
      );
      await click(button("Cancel"));
      await act(async () =>
        host
          .querySelector("form")!
          .dispatchEvent(
            new Event("submit", { bubbles: true, cancelable: true }),
          ),
      );
      assert(
        closed === 0 && submitted === 0,
        "in-flight mutation can be hidden or repeated",
      );
    },
  ],
  [
    "Confirm has modal semantics and guards pending cancellation",
    async () => {
      let closed = 0;
      await mount(
        <Confirm
          title="Delete"
          body="Permanent"
          confirmLabel="Delete"
          busy
          error={null}
          onConfirm={() => {}}
          onClose={() => closed++}
        />,
        defaults,
        "/",
        false,
      );
      assert(
        host.querySelector('[role="dialog"],dialog[open]'),
        "confirmation has no dialog semantics",
      );
      await click(button("Keep it"));
      assert(closed === 0, "pending delete dismissed");
    },
  ],
  [
    "Organization admin can open the existing Add member workflow",
    async () => {
      await mount(<Orgs />);
      await click(button("Add member"));
      assert(
        host.textContent?.includes("Add a member"),
        "member form unreachable",
      );
    },
  ],
  [
    "Account reports revoke failures",
    async () => {
      await mount(<Account />, (p, init) => {
        if (init?.method === "DELETE") return failure();
        if (p.endsWith("/tokens"))
          return {
            tokens: [{ id: "pat", name: "cli", scopes: ["repo:read"] }],
          };
        if (p.endsWith("/ssh-keys")) return { keys: [] };
        return defaults(p);
      });
      await click(button("Revoke"));
      assert(
        host.textContent?.includes("Request failed (503)"),
        "revocation failure hidden",
      );
    },
  ],
  [
    "Secrets report revoke failures without fabricated status",
    async () => {
      await mount(<Secrets />, (p, init) => {
        if (init?.method === "DELETE") return failure();
        if (p.endsWith("/secrets"))
          return { secrets: [{ name: "TOKEN", environment: "production" }] };
        if (p.endsWith("/leases"))
          return {
            leases: [
              {
                id: "lease",
                secret_name: "TOKEN",
                run_id: "run",
                state: "issued",
                expires_at: "2026-01-01T10:00:00Z",
              },
            ],
          };
        return defaults(p);
      });
      assert(
        !host.textContent?.includes("failed"),
        "environment invents a failed status",
      );
      await click(button("Revoke"));
      assert(
        host.textContent?.includes("Request failed (503)"),
        "lease revoke failure hidden",
      );
    },
  ],
  [
    "Repository paths encode literal question and hash characters",
    async () => {
      await mount(<Repos />, (p) =>
        p.includes("/tree/")
          ? { entries: [{ name: "why?#.txt", kind: "blob" }] }
          : p.includes("/blob/")
            ? new Response("hello")
            : defaults(p),
      );
      await click(button("why?#.txt"));
      assert(
        requests.some((r) => r.endsWith("/blob/main/why%3F%23.txt")),
        "Git filename changes URL semantics",
      );
    },
  ],
  [
    "Repository switch clears prior file and branch state",
    async () => {
      await mount(<Repos />, (p) =>
        p.includes("/tree/")
          ? { entries: [{ name: "only-alpha.txt", kind: "blob" }] }
          : p.includes("/blob/")
            ? new Response("hello")
            : defaults(p),
      );
      await click(button("only-alpha.txt"));
      await click(button("beta"));
      assert(
        !requests.some((r) => r.includes("/repos/beta/blob/")),
        "old file queried in new repository",
      );
    },
  ],
  ...(
    [
      ["Knowledge", Knowledge],
      ["Settings", Settings],
    ] as const
  ).map(([name, Screen]): [string, () => Promise<void>] => [
    `${name} never silently selects first repository`,
    async () => {
      await mount(<Screen />, (p) =>
        p.includes("/knowledge?")
          ? { entries: [], mode: "recent" }
          : p.endsWith("/gates")
            ? { gates: [], ref: "main" }
            : defaults(p),
      );
      assert(
        !requests.some((r) => r.includes("/repos/alpha/")),
        `${name} acts on first repository in all-projects scope`,
      );
    },
  ]),
  [
    "Authentication server errors remain failures, not a login prompt",
    async () => {
      await mount(
        <App />,
        (p) => (p === "/api/v1/user" ? failure() : defaults(p)),
        "/",
        false,
      );
      assert(
        host.textContent?.includes("Request failed (503)"),
        "auth outage shown as sign-in",
      );
    },
  ],
  [
    "Failed sign-out preserves session and reports the failure",
    async () => {
      await mount(
        <App />,
        (p) => (p.endsWith("/logout") ? failure() : defaults(p)),
        "/work",
        false,
      );
      const before = storedToken();
      await click(button("Sign out"));
      assert(
        storedToken() === before,
        "failed revocation silently cleared local credentials",
      );
      assert(
        host.textContent?.includes("Request failed (503)"),
        "logout failure hidden",
      );
      assert(button("Sign out"), "signed out despite server failure");
    },
  ],
  [
    "Sign-out requests revocation and clears previous principal cache",
    async () => {
      await mount(
        <App />,
        (p) => (p.endsWith("/logout") ? {} : defaults(p)),
        "/work",
        false,
      );
      client.setQueryData(["private-previous-person"], { secret: "fixture" });
      await click(button("Sign out"));
      assert(
        requests.includes("POST /api/v1/auth/logout"),
        "revocation endpoint not requested",
      );
      assert(
        client.getQueryData(["private-previous-person"]) === undefined,
        "previous principal cache retained",
      );
    },
  ],
  [
    "Changing organization discards open repository mutation dialog",
    async () => {
      await mount(<App />, defaults, "/work", false);
      await click(button("New Work Item"));
      const picker = host.querySelector("button[aria-expanded]") as HTMLElement;
      assert(picker, "workspace picker lacks expanded semantics");
      await click(picker);
      const two = [...host.querySelectorAll("button")].find(
        (b) => b.textContent?.includes("two") && b.textContent?.includes("org"),
      );
      assert(two, "second organization missing");
      await click(two);
      assert(
        !host.querySelector('[role="dialog"],dialog[open]'),
        "old draft survives organization change",
      );
    },
  ],
];

export async function runGUIRegressions() {
  const originalFetch = window.fetch;
  const saved = Object.entries(localStorage);
  const results: { name: string; status: string; error?: string }[] = [];
  try {
    for (const [name, test] of cases) {
      localStorage.clear();
      setStoredToken("test-session");
      try {
        await test();
        results.push({ name, status: "passed" });
      } catch (e) {
        results.push({ name, status: "failed", error: String(e) });
      } finally {
        if (root) {
          await act(async () => root.unmount());
          root = undefined!;
        }
        client?.clear();
        host.replaceChildren();
      }
    }
  } finally {
    window.fetch = originalFetch;
    setStoredToken(null);
    localStorage.clear();
    for (const [k, v] of saved) localStorage.setItem(k, v);
  }
  return results;
}
Object.assign(window, { runGUIRegressions });
