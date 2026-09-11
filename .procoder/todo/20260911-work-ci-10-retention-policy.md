# work-ci 10: Retention policy

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 10 of `.procoder/plans/work-ci.md`, which exists to: Deliver Work Items, Engineering Runs, and the CI system: typed engineering intent that can be assigned to a human or an agent, pull requests that carry plan and proof rather than only a diff, and runners that receive pushed jobs, stream logs, and upload artifacts.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/retention/policy.go`, `internal/retention/policy_test.go`, `internal/retention/migrations/000001_retention.up.sql`, `internal/retention/migrations/000001_retention.down.sql`

Interfaces: produces `retention.Policy{OrgID uuid.UUID, LogDays int, EvidenceDays int}` with `retention.Store.Get(ctx, orgID uuid.UUID) (Policy, error)` returning the default `{LogDays: 90, EvidenceDays: 0}` when unset, where `0` means keep indefinitely, and `retention.Sweeper.Sweep(ctx context.Context) (deleted int, err error)`.

## Acceptance criteria

- [x] Write the up migration creating `retention_policies` (org_id uuid primary key, log_days int not null default 90 check (log_days >= 0), evidence_days int not null default 0 check (evidence_days >= 0)) and the matching down migration.
- [x] Write the failing test `internal/retention/policy_test.go`: `func TestDefaultPolicyWhenUnset(t *testing.T)` asserts an org with no row yields `LogDays == 90` and `EvidenceDays == 0`; `func TestSweepDeletesExpiredLogsOnly(t *testing.T)` seeds one sealed log 100 days old and one 10 days old plus a 100-day-old proof record, runs `Sweep`, and asserts the old log object is gone while both the recent log and the proof record remain; `func TestSweepHonoursZeroAsForever(t *testing.T)` asserts `EvidenceDays == 0` deletes no evidence at any age. Run `go test ./internal/retention/` — expect FAIL with "undefined: retention.Sweeper".
- [x] Implement `Sweep` iterating orgs, deleting sealed log objects and artifact objects older than `LogDays` through `blobstore.Delete` before deleting their rows, and skipping any category whose configured days is zero.
- [x] Run `TEST_DATABASE_URL=... TEST_S3_ENDPOINT=... go test ./internal/retention/` — expect PASS.
- [x] Commit as `feat: add per-org retention policy with sweeper`.

## Evidence

- Task 10: per-org retention with sane defaults and a sweeper. EvidenceDays is stored but enforced by the owning service, because the spec forbids one service reading another's tables.
- Built by a parallel agent in an isolated worktree under red-green TDD, merged to main, and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge): ok ci 7.880s, ok runner 1.059s, ok retention 3.456s, 0 failures. Real PostgreSQL 16, Redis 7 and MinIO in the kw cluster.
- A real flakiness source was found and fixed, not ignored: a blanket TRUNCATE per test raced against other suites sharing the external Postgres. Tests were rescoped to their own org, run and random bucket, with TRUNCATE reserved for the two that genuinely need exclusive access to the job queue.
- A real SECURITY defect was caught by the commit gate and fixed rather than suppressed: the runner executed the repository's workflow command directly on the runner host. A CI job runs whatever the workflow file says, so that command is attacker-controlled by construction. Jobs now run in their own Kubernetes pod built from the job's image, with the clone performed inside the pod; the host path requires NOVAFORGE_ALLOW_LOCAL_EXEC=1 and a runner outside a cluster without it refuses to start. TestLocalExecutionRefusedByDefault and TestPodExecutorCreatesIsolatedPod pin both halves.
- `go build ./...` and `go vet ./...` exit 0.
- Implementing commits: 87bfa97, 92f9da6, 36b23ed, 13d2b92, 4884078, 6c0bc8b, 58a0df0, b98555b, plus the isolation fix in d783e12.
