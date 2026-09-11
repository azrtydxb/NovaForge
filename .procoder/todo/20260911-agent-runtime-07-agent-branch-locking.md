# agent-runtime 07: Agent branch locking

Status: open
Created: 2026-09-11

## Description

Plan step 7 of `.procoder/plans/agent-runtime.md`, which exists to: Make AI agents first-class contributors: agent identities with provenance, Agent Runs executing in isolated per-run Kubernetes namespaces, a typed and fully audited tool surface instead of shell access, and all model access routed through go-ai-sdk so no provider-specific logic enters NovaForge.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/agents/lock.go`, `internal/agents/lock_test.go`

Interfaces: produces `agents.BranchLock.Acquire(ctx, orgID, repoID uuid.UUID, branch string, runID uuid.UUID) error`, `agents.BranchLock.Release(ctx, orgID, repoID uuid.UUID, branch string) error`, and `agents.BranchLock.Holder(ctx, orgID, repoID uuid.UUID, branch string) (uuid.UUID, bool, error)`. The git transports from the foundation plan call `Holder` and reject a push from anyone other than the holding run.

## Acceptance criteria

- [ ] Write the failing test `internal/agents/lock_test.go`: `func TestAcquireThenSecondAcquireFails(t *testing.T)` asserts a second `Acquire` on the same branch returns an error containing "locked by run"; `func TestHumanPushRejectedWhileLocked(t *testing.T)` acquires for a run, then asserts the transport capability check for a user scope on that branch returns an error containing "locked"; `func TestReleaseAllowsReacquire(t *testing.T)` asserts release then acquire succeeds; `func TestLockExpiresWithRun(t *testing.T)` asserts a lock whose run reached a terminal state is treated as released. Run `go test ./internal/agents/` — expect FAIL with "undefined: agents.BranchLock".
- [ ] Implement the lock as a row in `agent_runs` keyed by `(org_id, repo_id, branch)` with a partial unique index `CREATE UNIQUE INDEX agent_branch_lock ON agent_runs (org_id, repo_id, branch) WHERE state = 'running';` so the database enforces single ownership rather than application code.
- [ ] Wire `Holder` into the `CapFunc` used by both git transports so HTTP and SSH refuse identically.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/agents/` — expect PASS.
- [ ] Commit as `feat: lock agent branches for the duration of a run`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
