# governance 01: Gate definitions and evaluation schema

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 1 of `.procoder/plans/governance.md`, which exists to: Make the platform, not the agent, the authority on merge: a gate controller that runs after an agent declares completion and cannot be bypassed or self-approved, a policy-driven approval model the model can never alter, and a secret broker issuing short-lived scoped credentials instead of durable secrets.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/gates/migrations/000001_gates.up.sql`, `internal/gates/migrations/000001_gates.down.sql`, `internal/gates/store.go`, `internal/gates/store_test.go`

Interfaces: produces `gates.Definition{Name string, Params map[string]any, Required bool}`, `gates.Evaluation{ID, OrgID, RunID uuid.UUID, Gate string, Status string, Detail string, EvaluatedAt time.Time, TargetSHA string}`, and `gates.Store` with `RecordEvaluation(ctx, Evaluation) error`, `ListEvaluations(ctx, runID uuid.UUID) ([]Evaluation, error)`, and `LatestForSHA(ctx, runID uuid.UUID, sha string) (map[string]Evaluation, error)`.

## Acceptance criteria

- [x] Write the up migration creating `gate_evaluations` (id uuid pk, org_id uuid not null, run_id uuid not null, gate text not null, status text not null check (status in ('pass','fail','error','skipped')), detail text not null default '', target_sha text not null, evaluated_at timestamptz not null default now(), unique (run_id, gate, target_sha)) plus `CREATE INDEX ON gate_evaluations (org_id, run_id);` and the matching down migration.
- [x] Write the failing test `internal/gates/store_test.go`: `func TestRecordEvaluationUpserts(t *testing.T)` records `tests` as `fail` then as `pass` for the same run and SHA, and asserts exactly one row remains with status `pass` — proving an at-least-once redelivery cannot create duplicates; `func TestEvaluationsAreShaScoped(t *testing.T)` records a pass for SHA `aaa`, then asserts `LatestForSHA(run, "bbb")` returns no entry for that gate, so a new push invalidates prior results; `func TestListIsOrgScoped(t *testing.T)` asserts a list under org A never returns org B's evaluations. Run `go test ./internal/gates/` — expect FAIL with "undefined: gates.Store".
- [x] Implement `RecordEvaluation` as `INSERT ... ON CONFLICT (run_id, gate, target_sha) DO UPDATE SET status=EXCLUDED.status, detail=EXCLUDED.detail, evaluated_at=now()`.
- [x] Run `TEST_DATABASE_URL=... go test ./internal/gates/` — expect PASS.
- [x] Commit as `feat: add gate evaluation schema scoped to run and commit`.

## Evidence

- governance Task 1: gate_evaluations upserting on (run_id, gate, target_sha), so redelivery cannot duplicate and a new push invalidates prior results.
- Built by a parallel agent in an isolated git worktree under strict red-green TDD, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge by the main agent): 18 PASS, 0 FAIL. ok agents 1.613s, ok workspace 1.425s, ok gates 1.418s.
- TestBudgetConcurrentToolCallsAreSafe runs 100 goroutines and was additionally verified by the implementing agent under -race, clean.
- Postgres tests ran against the REAL PostgreSQL 16 in the kw cluster. Kubernetes tests use k8s.io/client-go/kubernetes/fake, which is the official clientset fake and the correct way to assert on created objects.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: ba4def9, d45a2d9, ffe166a, 3a49552, 23db4d3.
