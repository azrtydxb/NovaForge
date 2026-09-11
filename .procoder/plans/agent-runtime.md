# agent-runtime — implementation plan

Status: complete
Spec: .procoder/specs/backend-platform.md

## Goal

Make AI agents first-class contributors: agent identities with provenance, Agent Runs executing
in isolated per-run Kubernetes namespaces, a typed and fully audited tool surface instead of
shell access, and all model access routed through go-ai-sdk so no provider-specific logic
enters NovaForge.

## Architecture

One new Go service, `agent-runtime`, owning its own PostgreSQL schema and gRPC API. It creates
a Kubernetes namespace per Agent Run, mounts a Git worktree scoped to the run's capability
grant, and exposes the typed tools over gRPC to the agent process inside that namespace. Every
tool call is authorized against the run's grant and written to an append-only audit log before
it executes.

## Constraints

Taken verbatim from the spec; every task inherits these.

- Go 1.26 or later, PostgreSQL 16 or later with pgvector, Redis 7 or later with Streams and
  consumer groups, Kubernetes 1.29 or later.
- Internal transport is gRPC; the client edge is REST described by OpenAPI; events are Redis
  Streams.
- One PostgreSQL cluster, one schema per service. No service reads another service's tables;
  cross-service reads go through that service's gRPC API.
- Organizations are a hard security boundary: per-org namespaces, per-org credentials, and no
  data path between orgs. Every authorization check is org-scoped, and no query may be
  satisfiable without an org predicate.
- Air-gapped operation is a first-class deployment model. No feature may hard-depend on
  reaching a hosted model provider or the public internet.
- No provider-specific AI logic in NovaForge — model access is exclusively through go-ai-sdk.
- Evidence, not chain-of-thought: only observable actions and artifacts are stored.
- Agents are never issued a broad token; every permission comes from a capability grant.
- Agent runs are bounded by wall-clock, token, and cost limits, and exceeding any limit
  terminates the run while retaining its evidence.
- An agent branch is locked for the duration of a run; human pushes to it are rejected.
- Redis durability is AOF persistence with consumer-group redelivery, giving at-least-once
  delivery. Every stream handler must be idempotent.
- Kubernetes and Helm are the only supported deployment path; docker-compose is out of scope.
- Module path is `github.com/novaforge/novaforge`. Service binaries live under `cmd/<service>`,
  shared packages under `internal/<domain>`.

## Task 1: Agent identity and provenance schema

Files: `internal/agents/migrations/000001_agents.up.sql`,
`internal/agents/migrations/000001_agents.down.sql`, `internal/agents/store.go`,
`internal/agents/store_test.go`

Interfaces: produces `agents.Agent{ID, OrgID uuid.UUID, Name, Role string, ModelRef string, Enabled bool}`,
`agents.Run{ID, OrgID, AgentID, WorkItemID, SponsorID uuid.UUID, GrantID uuid.UUID, Branch string, State string, StartedAt time.Time, EndedAt *time.Time, WallclockLimit time.Duration, TokenLimit int64, CostLimitMicros int64}`,
and `agents.Store` with `CreateAgent`, `GetAgent`, `ListAgents`, `CreateRun`, `GetRun`,
`SetRunState`, and `RecordProvenance(ctx, runID uuid.UUID, p Provenance) error` where
`Provenance` is `{AgentName, ModelName, WorkItemKey, RunRef, SponsorName string}`.

- [ ] Write the failing test `internal/agents/store_test.go`:
      `func TestCreateRunRecordsProvenance(t *testing.T)` creates a run, records provenance, and
      asserts `GetRun` returns the agent name, model name, and sponsor;
      `func TestListAgentsIsOrgScoped(t *testing.T)` creates agents in two orgs and asserts a
      list for org A never returns org B's;
      `func TestRunStateTransitions(t *testing.T)` asserts `SetRunState` accepts
      `queued`→`running`→`succeeded` and rejects `succeeded`→`running` with an error containing
      "invalid transition". Run `go test ./internal/agents/` — expect FAIL with "undefined:
      agents.Store".
- [ ] Write the up migration creating `agents` (id uuid pk, org_id uuid not null, name text not
      null, role text not null, model_ref text not null, enabled boolean not null default true,
      unique (org_id, name)), `agent_runs` (id uuid pk, org_id uuid not null, agent_id uuid not
      null references agents(id), work_item_id uuid, sponsor_id uuid not null, grant_id uuid not
      null, branch text not null, state text not null default 'queued' check (state in
      ('queued','running','succeeded','failed','cancelled','over_budget')), started_at
      timestamptz, ended_at timestamptz, wallclock_limit_seconds int not null default 3600,
      token_limit bigint not null default 1000000, cost_limit_micros bigint not null default
      5000000), and `run_provenance` (run_id uuid primary key references agent_runs(id) on
      delete cascade, agent_name text not null, model_name text not null, work_item_key text,
      run_ref text not null, sponsor_name text not null); plus the matching down migration.
- [ ] Implement `SetRunState` with a transition table permitting only
      queued→{running,cancelled}, running→{succeeded,failed,cancelled,over_budget}, and nothing
      out of a terminal state, returning
      `fmt.Errorf("invalid transition %s -> %s", from, to)` otherwise.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/agents/` — expect PASS.
- [ ] Commit as `feat: add agent identity and run provenance schema`.

## Task 2: Tool-call audit log

Files: `internal/agents/migrations/000002_audit.up.sql`,
`internal/agents/migrations/000002_audit.down.sql`, `internal/agents/audit.go`,
`internal/agents/audit_test.go`

Interfaces: produces
`agents.AuditLog.Record(ctx, e Entry) (uuid.UUID, error)`,
`agents.AuditLog.Complete(ctx, id uuid.UUID, outcome string, errText string) error`, and
`agents.AuditLog.List(ctx, runID uuid.UUID) ([]Entry, error)`, where `Entry` is
`{ID, RunID uuid.UUID, Tool string, ArgsJSON []byte, Outcome string, Error string, StartedAt, EndedAt time.Time}`.
Every tool in Task 5 calls `Record` before executing and `Complete` after.

- [ ] Write the up migration creating `tool_calls` (id uuid pk, run_id uuid not null references
      agent_runs(id) on delete cascade, org_id uuid not null, tool text not null, args_json
      jsonb not null, outcome text not null default 'pending', error text not null default '',
      started_at timestamptz not null default now(), ended_at timestamptz) plus
      `CREATE INDEX ON tool_calls (run_id, started_at);` and the matching down migration.
- [ ] Write the failing test `internal/agents/audit_test.go`:
      `func TestRecordThenComplete(t *testing.T)` records a call to tool `repo.read_file`,
      completes it with outcome `ok`, and asserts `List` returns one entry with both the args
      and the outcome;
      `func TestArgsAreStoredVerbatim(t *testing.T)` records args
      `{"path":"cmd/main.go","ref":"main"}` and asserts the stored JSON round-trips exactly;
      `func TestFailedCallRetainsError(t *testing.T)` completes with outcome `error` and text
      `permission denied` and asserts both persist. Run `go test ./internal/agents/` — expect
      FAIL with "undefined: agents.AuditLog".
- [ ] Implement `Record` and `Complete` as plain inserts and updates with no delete path
      exposed, so the log is append-only from the application's side.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/agents/` — expect PASS.
- [ ] Commit as `feat: add append-only tool call audit log`.

## Task 3: Budget enforcement

Files: `internal/agents/budget.go`, `internal/agents/budget_test.go`

Interfaces: produces `agents.Budget` with
`NewBudget(wallclock time.Duration, tokens int64, costMicros int64) *Budget`,
`(*Budget) AddTokens(n int64)`, `(*Budget) AddCostMicros(n int64)`, and
`(*Budget) Check() error` returning `agents.ErrOverBudget` wrapped with the exceeded dimension.
The run loop in Task 6 calls `Check` before every tool call and before every model call.

- [ ] Write the failing test `internal/agents/budget_test.go`:
      `func TestWallclockExceeded(t *testing.T)` builds a budget with a 10ms wall-clock limit,
      sleeps 30ms, and asserts `Check` returns an error satisfying
      `errors.Is(err, agents.ErrOverBudget)` and containing "wallclock";
      `func TestTokenLimitExceeded(t *testing.T)` adds 101 tokens against a limit of 100 and
      asserts the error contains "tokens";
      `func TestCostLimitExceeded(t *testing.T)` asserts the same for cost;
      `func TestUnderBudgetPasses(t *testing.T)` asserts `Check` is nil when every dimension is
      below its limit. Run `go test ./internal/agents/` — expect FAIL with "undefined:
      agents.Budget".
- [ ] Implement `Budget` with an atomic counter per dimension and a `started time.Time`, so
      concurrent tool calls accounting against the same budget are safe.
- [ ] Run `go test ./internal/agents/` — expect PASS.
- [ ] Commit as `feat: add agent run budget enforcement`.

## Task 4: Kubernetes workspace isolation

Files: `internal/workspace/k8s.go`, `internal/workspace/k8s_test.go`

Interfaces: produces `workspace.Provisioner` with
`Create(ctx context.Context, runID uuid.UUID, spec Spec) (Workspace, error)`,
`Destroy(ctx context.Context, runID uuid.UUID) error`, and
`Reap(ctx context.Context, olderThan time.Duration) (int, error)`, where `Spec` is
`{Image string, Env map[string]string, CPULimit, MemLimit string, RepoPVC string}` and
`Workspace` is `{Namespace string, PodName string}`. Namespaces are named `nf-run-<runID>`.

- [ ] Write the failing test `internal/workspace/k8s_test.go` using
      `k8s.io/client-go/kubernetes/fake`:
      `func TestCreateMakesNamespacedPod(t *testing.T)` asserts a namespace `nf-run-<id>` and a
      pod inside it are created, and that the pod carries the label
      `novaforge.io/run-id=<id>`;
      `func TestCreateAppliesDenyAllNetworkPolicy(t *testing.T)` asserts a NetworkPolicy exists
      in the namespace with an empty pod selector and no ingress rules;
      `func TestDestroyRemovesNamespace(t *testing.T)` asserts the namespace is gone after
      `Destroy`;
      `func TestReapRemovesOrphanedNamespaces(t *testing.T)` creates a namespace labelled with a
      creation timestamp two hours old and asserts `Reap(ctx, time.Hour)` deletes it and returns 1. Run `go test ./internal/workspace/` — expect FAIL with "undefined:
      workspace.Provisioner".
- [ ] Add `go get k8s.io/client-go` and implement `Create`: create the namespace with labels
      `novaforge.io/run-id` and `novaforge.io/created-at`, apply a default-deny NetworkPolicy,
      apply a ResourceQuota from `Spec`, then create the pod mounting the repository PVC
      read-only.
- [ ] Implement `Reap` listing namespaces with the `novaforge.io/run-id` label whose
      `novaforge.io/created-at` is older than the threshold and deleting them, so a crashed
      controller never leaks workspaces.
- [ ] Run `go test ./internal/workspace/` — expect PASS.
- [ ] Commit as `feat: provision isolated per-run kubernetes workspaces`.

## Task 5: Typed agent tools

Files: `proto/tools/v1/tools.proto`, `internal/tools/registry.go`, `internal/tools/repo.go`,
`internal/tools/workspace.go`, `internal/tools/git.go`, `internal/tools/ci.go`,
`internal/tools/work.go`, `internal/tools/registry_test.go`, `internal/tools/git_test.go`

Interfaces: produces `tools.Registry` with
`Register(name string, h Handler)`, `Call(ctx context.Context, runID uuid.UUID, name string, argsJSON []byte) ([]byte, error)`,
and `Names() []string`, where `type Handler func(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error)`
and `Runtime` carries the run's `capability.Grant`, its `*agents.Budget`, and clients for the
git, work, reviews, and ci services. The thirteen registered names are exactly
repo.search, repo.read_file, repo.get_symbol, repo.get_dependencies, workspace.write_file,
git.diff, git.commit, ci.run_test, ci.get_logs, work.get, work.comment, architecture.query,
and gate.status.

- [ ] Write the failing test `internal/tools/registry_test.go`:
      `func TestRegistryHasExactlyThirteenTools(t *testing.T)` asserts `Names()` equals the
      thirteen names above, sorted — so adding an unlisted tool fails the build;
      `func TestUnknownToolRejected(t *testing.T)` asserts `Call` with name `"shell.exec"`
      returns an error containing "unknown tool";
      `func TestEveryCallIsAudited(t *testing.T)` calls `work.get` and asserts exactly one audit
      entry exists for the run with that tool name;
      `func TestBudgetCheckedBeforeCall(t *testing.T)` exhausts the budget and asserts `Call`
      returns `agents.ErrOverBudget` without the handler ever running. Run
      `go test ./internal/tools/` — expect FAIL with "undefined: tools.Registry".
- [ ] Write the failing test `internal/tools/git_test.go`:
      `func TestGitCommitRefusesOutOfScopeBranch(t *testing.T)` builds a Runtime whose grant
      allows `agents/NF-1/` and asserts `git.commit` targeting `refs/heads/main` returns an
      error containing "not permitted", proving the tool path refuses identically to the
      transport path;
      `func TestWorkspaceWriteFileRejectsTraversal(t *testing.T)` asserts
      `workspace.write_file` with path `../../etc/passwd` returns an error containing "outside
      workspace".
- [ ] Implement `Call` so the order is fixed and testable: budget check, audit `Record`,
      capability check, handler, audit `Complete`. A handler is never reached when any earlier
      step fails.
- [ ] Implement each handler as a thin translation onto the existing gRPC services, with
      `git.commit` routing through `capability.CanWriteRef` and `workspace.write_file`
      resolving the path with `filepath.Clean` and rejecting any result escaping the workspace
      root.
- [ ] Run `go test ./internal/tools/` — expect PASS.
- [ ] Commit as `feat: add thirteen typed agent tools with audited dispatch`.

## Task 6: go-ai-sdk integration and the agent run loop

Files: `internal/agentrun/loop.go`, `internal/agentrun/loop_test.go`,
`internal/agentrun/model.go`

Interfaces: produces
`agentrun.Loop.Execute(ctx context.Context, run agents.Run, reg *tools.Registry) (Result, error)`
where `Result` is `{State string, Steps int, TokensUsed int64, Summary string}`, and
`agentrun.NewModelClient(cfg ModelConfig) (aisdk.Model, error)` with `ModelConfig` carrying
`Provider, Endpoint, Model string`. `NewModelClient` is the only place in NovaForge that names
a provider, and it does so by passing the string through to go-ai-sdk.

- [ ] Write the failing test `internal/agentrun/loop_test.go`:
      `func TestLoopStopsOnBudget(t *testing.T)` drives the loop with a stub model that always
      requests another tool call and a 3-step budget, asserting the result state is
      `over_budget` and that provenance was still recorded;
      `func TestLoopRecordsEvidenceNotReasoning(t *testing.T)` drives a stub model returning
      both reasoning text and a tool call, then asserts the persisted rows contain the tool call
      and contain no field holding the reasoning text;
      `func TestLoopSurfacesToolErrors(t *testing.T)` asserts a tool returning an error is fed
      back to the model as a tool result rather than aborting the run. Run
      `go test ./internal/agentrun/` — expect FAIL with "undefined: agentrun.Loop".
- [ ] Add `go get github.com/azrtydxb/go-ai-sdk` and implement `NewModelClient` selecting the
      provider purely by the configured string, with no per-provider branch beyond passing
      `Endpoint` and `Model` through.
- [ ] Implement `Execute` as the loop: assemble the request, call the model through go-ai-sdk,
      dispatch any requested tool through `reg.Call`, append the result, and repeat until the
      model returns no tool call or the budget check fails.
- [ ] Persist only observable actions: the tool calls through the audit log and a final summary
      string. Never write model reasoning to any table.
- [ ] Run `go test ./internal/agentrun/` — expect PASS.
- [ ] Commit as `feat: add agent run loop over go-ai-sdk`.

## Task 7: Agent branch locking

Files: `internal/agents/lock.go`, `internal/agents/lock_test.go`

Interfaces: produces `agents.BranchLock.Acquire(ctx, orgID, repoID uuid.UUID, branch string, runID uuid.UUID) error`,
`agents.BranchLock.Release(ctx, orgID, repoID uuid.UUID, branch string) error`, and
`agents.BranchLock.Holder(ctx, orgID, repoID uuid.UUID, branch string) (uuid.UUID, bool, error)`.
The git transports from the foundation plan call `Holder` and reject a push from anyone other
than the holding run.

- [ ] Write the failing test `internal/agents/lock_test.go`:
      `func TestAcquireThenSecondAcquireFails(t *testing.T)` asserts a second `Acquire` on the
      same branch returns an error containing "locked by run";
      `func TestHumanPushRejectedWhileLocked(t *testing.T)` acquires for a run, then asserts the
      transport capability check for a user scope on that branch returns an error containing
      "locked";
      `func TestReleaseAllowsReacquire(t *testing.T)` asserts release then acquire succeeds;
      `func TestLockExpiresWithRun(t *testing.T)` asserts a lock whose run reached a terminal
      state is treated as released. Run `go test ./internal/agents/` — expect FAIL with
      "undefined: agents.BranchLock".
- [ ] Implement the lock as a row in `agent_runs` keyed by `(org_id, repo_id, branch)` with a
      partial unique index `CREATE UNIQUE INDEX agent_branch_lock ON agent_runs (org_id, repo_id, branch) WHERE state = 'running';`
      so the database enforces single ownership rather than application code.
- [ ] Wire `Holder` into the `CapFunc` used by both git transports so HTTP and SSH refuse
      identically.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/agents/` — expect PASS.
- [ ] Commit as `feat: lock agent branches for the duration of a run`.

## Task 8: Repository configuration under .novaforge

Files: `internal/repoconfig/config.go`, `internal/repoconfig/config_test.go`

Interfaces: produces `repoconfig.Project{Name string, DefaultAgent string, Gates []string}`,
`repoconfig.AgentDef{Name, Role, Model string, Tools []string}`,
`repoconfig.GateDef{Name string, Params map[string]any}`, and
`repoconfig.Load(ctx context.Context, git gitv1.GitServiceClient, orgID, repoID uuid.UUID, ref string) (Config, error)`
where `Config` holds the project, the agents, the gates, and the context document paths.

- [ ] Write the failing test `internal/repoconfig/config_test.go`:
      `func TestLoadParsesProjectAndAgents(t *testing.T)` stubs the git client to return a
      project document and one agent document and asserts both parse;
      `func TestMalformedConfigFailsLoudly(t *testing.T)` returns invalid YAML and asserts the
      error contains "novaforge config" and that no partially populated `Config` is returned —
      a malformed config must never silently disable enforcement;
      `func TestAbsentConfigIsNotAnError(t *testing.T)` asserts a repository with no .novaforge
      directory yields the zero `Config` and a nil error;
      `func TestUnknownAgentToolRejected(t *testing.T)` asserts an agent listing a tool outside
      the thirteen registered names errors with "unknown tool". Run
      `go test ./internal/repoconfig/` — expect FAIL with "undefined: repoconfig.Load".
- [ ] Implement `Load` reading, through the git service, the paths .novaforge/project.yaml, then
      every file under .novaforge/agents/ and .novaforge/gates/, and listing .novaforge/context/
      without reading it.
- [ ] Validate agent tool lists against `tools.Registry.Names()` so configuration cannot request
      a tool the platform does not implement.
- [ ] Run `go test ./internal/repoconfig/` — expect PASS.
- [ ] Commit as `feat: load repository agent and gate configuration from .novaforge`.

## Task 9: agent-runtime service, events, and deployment

Files: `proto/agents/v1/agents.proto`, `cmd/agent-runtime/main.go`, `internal/agents/grpc.go`,
`internal/agents/grpc_test.go`, `internal/edge/agent_routes.go`, `api/openapi.yaml`,
`Dockerfile.agent-runtime`, `deploy/helm/novaforge/templates/agent-runtime.yaml`,
`deploy/helm/novaforge/templates/agent-rbac.yaml`, `tests/e2e/agent_run_test.sh`

Interfaces: produces `novaforge.agents.v1.AgentService` with `CreateAgent`, `ListAgents`,
`StartRun`, `GetRun`, `CancelRun`, and `StreamRunEvents`, plus the Redis stream constant
`events.StreamAgentEvents = "stream:agent:events"` carrying one message per run state change
and per tool call.

- [ ] Extend `api/openapi.yaml` with `GET|POST /api/v1/orgs/{org}/agents`,
      `POST /api/v1/orgs/{org}/repos/{repo}/agent-runs`,
      `GET /api/v1/orgs/{org}/repos/{repo}/agent-runs/{id}`,
      `GET /api/v1/orgs/{org}/repos/{repo}/agent-runs/{id}/events`, and
      `GET /api/v1/orgs/{org}/repos/{repo}/agent-runs/{id}/tool-calls`.
- [ ] Write the failing test `internal/agents/grpc_test.go`:
      `func TestStartRunIssuesScopedGrant(t *testing.T)` starts a run for Work Item `NF-1` and
      asserts the issued grant's `WriteBranch` is exactly `agents/NF-1/` and that
      `secrets_prod` and `deploy_prod` are false;
      `func TestStartRunDeniedWithoutSponsor(t *testing.T)` asserts a start with no human sponsor
      returns `codes.PermissionDenied`;
      `func TestStreamRunEventsDeliversStateChanges(t *testing.T)` asserts a state change from
      queued to running is delivered on the stream. Run `go test ./internal/agents/` — expect
      FAIL with "undefined: agents.NewGRPCServer".
- [ ] Implement `cmd/agent-runtime/main.go`: migrate schema `agents`, serve gRPC on 9095, start
      the workspace reaper on a 5m ticker calling `Reap(ctx, time.Hour)`, expose `/healthz`, and
      drain for 30s on SIGTERM.
- [ ] Write `agent-rbac.yaml` granting the service a Role limited to creating, listing, and
      deleting namespaces, pods, networkpolicies, and resourcequotas bearing the
      `novaforge.io/run-id` label, and nothing else.
- [ ] Write the failing test `tests/e2e/agent_run_test.sh`: on the kind cluster, start an agent
      run against a seeded repository with a stub model endpoint, assert a namespace matching
      `nf-run-*` appears while the run is active, assert the run reaches `succeeded` within 300s,
      assert the namespace is gone afterwards, and assert `nf agent-run tool-calls <id>` lists at
      least one recorded call. Run `bash tests/e2e/agent_run_test.sh` — expect FAIL with
      "Error: no matching deployment novaforge-agent-runtime".
- [ ] Write the Deployment template and Dockerfile, then run
      `bash tests/e2e/agent_run_test.sh` — expect PASS.
- [ ] Commit as `feat: add agent-runtime service with isolated runs and event streaming`.
