# agent-runtime 01: Agent identity and provenance schema

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 1 of `.procoder/plans/agent-runtime.md`, which exists to: Make AI agents first-class contributors: agent identities with provenance, Agent Runs executing in isolated per-run Kubernetes namespaces, a typed and fully audited tool surface instead of shell access, and all model access routed through go-ai-sdk so no provider-specific logic enters NovaForge.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/agents/migrations/000001_agents.up.sql`, `internal/agents/migrations/000001_agents.down.sql`, `internal/agents/store.go`, `internal/agents/store_test.go`

Interfaces: produces `agents.Agent{ID, OrgID uuid.UUID, Name, Role string, ModelRef string, Enabled bool}`, `agents.Run{ID, OrgID, AgentID, WorkItemID, SponsorID uuid.UUID, GrantID uuid.UUID, Branch string, State string, StartedAt time.Time, EndedAt *time.Time, WallclockLimit time.Duration, TokenLimit int64, CostLimitMicros int64}`, and `agents.Store` with `CreateAgent`, `GetAgent`, `ListAgents`, `CreateRun`, `GetRun`, `SetRunState`, and `RecordProvenance(ctx, runID uuid.UUID, p Provenance) error` where `Provenance` is `{AgentName, ModelName, WorkItemKey, RunRef, SponsorName string}`.

## Acceptance criteria

- [x] Write the failing test `internal/agents/store_test.go`: `func TestCreateRunRecordsProvenance(t *testing.T)` creates a run, records provenance, and asserts `GetRun` returns the agent name, model name, and sponsor; `func TestListAgentsIsOrgScoped(t *testing.T)` creates agents in two orgs and asserts a list for org A never returns org B's; `func TestRunStateTransitions(t *testing.T)` asserts `SetRunState` accepts `queued`→`running`→`succeeded` and rejects `succeeded`→`running` with an error containing "invalid transition". Run `go test ./internal/agents/` — expect FAIL with "undefined: agents.Store".
- [x] Write the up migration creating `agents` (id uuid pk, org_id uuid not null, name text not null, role text not null, model_ref text not null, enabled boolean not null default true, unique (org_id, name)), `agent_runs` (id uuid pk, org_id uuid not null, agent_id uuid not null references agents(id), work_item_id uuid, sponsor_id uuid not null, grant_id uuid not null, branch text not null, state text not null default 'queued' check (state in ('queued','running','succeeded','failed','cancelled','over_budget')), started_at timestamptz, ended_at timestamptz, wallclock_limit_seconds int not null default 3600, token_limit bigint not null default 1000000, cost_limit_micros bigint not null default 5000000), and `run_provenance` (run_id uuid primary key references agent_runs(id) on delete cascade, agent_name text not null, model_name text not null, work_item_key text, run_ref text not null, sponsor_name text not null); plus the matching down migration.
- [x] Implement `SetRunState` with a transition table permitting only queued→{running,cancelled}, running→{succeeded,failed,cancelled,over_budget}, and nothing out of a terminal state, returning `fmt.Errorf("invalid transition %s -> %s", from, to)` otherwise.
- [x] Run `TEST_DATABASE_URL=... go test ./internal/agents/` — expect PASS.
- [x] Commit as `feat: add agent identity and run provenance schema`.

## Evidence

- agent-runtime Task 1: agents and agent_runs schema with run_provenance; SetRunState enforces the transition table inside a SELECT FOR UPDATE transaction and refuses to leave a terminal state.
- Built by a parallel agent in an isolated git worktree under strict red-green TDD, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge by the main agent): 18 PASS, 0 FAIL. ok agents 1.613s, ok workspace 1.425s, ok gates 1.418s.
- TestBudgetConcurrentToolCallsAreSafe runs 100 goroutines and was additionally verified by the implementing agent under -race, clean.
- Postgres tests ran against the REAL PostgreSQL 16 in the kw cluster. Kubernetes tests use k8s.io/client-go/kubernetes/fake, which is the official clientset fake and the correct way to assert on created objects.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: ba4def9, d45a2d9, ffe166a, 3a49552, 23db4d3.
