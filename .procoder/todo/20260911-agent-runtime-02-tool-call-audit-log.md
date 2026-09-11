# agent-runtime 02: Tool-call audit log

Status: open
Created: 2026-09-11

## Description

Plan step 2 of `.procoder/plans/agent-runtime.md`, which exists to: Make AI agents first-class contributors: agent identities with provenance, Agent Runs executing in isolated per-run Kubernetes namespaces, a typed and fully audited tool surface instead of shell access, and all model access routed through go-ai-sdk so no provider-specific logic enters NovaForge.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/agents/migrations/000002_audit.up.sql`, `internal/agents/migrations/000002_audit.down.sql`, `internal/agents/audit.go`, `internal/agents/audit_test.go`

Interfaces: produces `agents.AuditLog.Record(ctx, e Entry) (uuid.UUID, error)`, `agents.AuditLog.Complete(ctx, id uuid.UUID, outcome string, errText string) error`, and `agents.AuditLog.List(ctx, runID uuid.UUID) ([]Entry, error)`, where `Entry` is `{ID, RunID uuid.UUID, Tool string, ArgsJSON []byte, Outcome string, Error string, StartedAt, EndedAt time.Time}`. Every tool in Task 5 calls `Record` before executing and `Complete` after.

## Acceptance criteria

- [ ] Write the up migration creating `tool_calls` (id uuid pk, run_id uuid not null references agent_runs(id) on delete cascade, org_id uuid not null, tool text not null, args_json jsonb not null, outcome text not null default 'pending', error text not null default '', started_at timestamptz not null default now(), ended_at timestamptz) plus `CREATE INDEX ON tool_calls (run_id, started_at);` and the matching down migration.
- [ ] Write the failing test `internal/agents/audit_test.go`: `func TestRecordThenComplete(t *testing.T)` records a call to tool `repo.read_file`, completes it with outcome `ok`, and asserts `List` returns one entry with both the args and the outcome; `func TestArgsAreStoredVerbatim(t *testing.T)` records args `{"path":"cmd/main.go","ref":"main"}` and asserts the stored JSON round-trips exactly; `func TestFailedCallRetainsError(t *testing.T)` completes with outcome `error` and text `permission denied` and asserts both persist. Run `go test ./internal/agents/` — expect FAIL with "undefined: agents.AuditLog".
- [ ] Implement `Record` and `Complete` as plain inserts and updates with no delete path exposed, so the log is append-only from the application's side.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/agents/` — expect PASS.
- [ ] Commit as `feat: add append-only tool call audit log`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
