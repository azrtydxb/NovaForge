# work-ci 05: CI schema, scheduler, and push consumer

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 5 of `.procoder/plans/work-ci.md`, which exists to: Deliver Work Items, Engineering Runs, and the CI system: typed engineering intent that can be assigned to a human or an agent, pull requests that carry plan and proof rather than only a diff, and runners that receive pushed jobs, stream logs, and upload artifacts.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/ci/migrations/000001_ci.up.sql`, `internal/ci/migrations/000001_ci.down.sql`, `internal/ci/store.go`, `internal/ci/scheduler.go`, `internal/ci/scheduler_test.go`

Interfaces: produces `ci.Store` with `CreateRun`, `GetRun`, `ListRuns`, `CreateJob`, `ClaimJob(ctx, runnerID uuid.UUID, labels []string) (Job, error)`, `SetJobStatus(ctx, jobID uuid.UUID, status string) error`, and `ci.Scheduler.Run(ctx context.Context) error` which consumes `events.StreamGitPush` in the consumer group `ci-engine`.

## Acceptance criteria

- [x] Write the up migration creating `workflow_runs` (id uuid pk, org_id uuid not null, repo_id uuid not null, commit_sha text not null, ref text not null, status text not null default 'queued' check (status in ('queued','running','success','failure','cancelled')), created_at timestamptz not null default now(), unique (repo_id, commit_sha, ref)), `workflow_jobs` (id uuid pk, run_id uuid not null references workflow_runs(id) on delete cascade, name text not null, needs text[] not null default '{}', run_cmd text, agent_role text, image text, status text not null default 'pending', runner_id uuid, started_at timestamptz, finished_at timestamptz, unique (run_id, name)), and `runners` (id uuid pk, org_id uuid not null, name text not null, labels text[] not null default '{}', token_hash bytea not null unique, last_seen_at timestamptz); plus the down migration.
- [x] Write the failing test `internal/ci/scheduler_test.go`: `func TestPushSchedulesRun(t *testing.T)` publishes a `PushEvent` to the stream with a stubbed git client returning a workflow with one job, runs the scheduler for one iteration, and asserts a `workflow_runs` row exists with that commit SHA; `func TestDuplicatePushIsIdempotent(t *testing.T)` publishes the identical event twice and asserts exactly one run row exists — proving the handler tolerates at-least-once redelivery; `func TestJobBlockedUntilNeedsSucceed(t *testing.T)` asserts `ClaimJob` never returns a job whose `needs` are not all `success`; `func TestMissingWorkflowSkipsSilently(t *testing.T)` asserts a push to a repository with no `.novaforge/workflow.yaml` creates no run and returns no error. Run `go test ./internal/ci/` — expect FAIL with "undefined: ci.Scheduler".
- [x] Implement the scheduler loop: `events.EnsureGroup`, then `XREADGROUP` with `Block` of 5s and `Count` of 10, `XACK` only after the handler returns nil, and an `XAUTOCLAIM` pass every 30s reclaiming messages idle longer than 60s.
- [x] Implement idempotency by making run creation an `INSERT ... ON CONFLICT (repo_id, commit_sha, ref) DO NOTHING RETURNING id` and treating a zero-row result as "already scheduled, nothing to do".
- [x] Implement `ClaimJob` as `SELECT ... FOR UPDATE SKIP LOCKED` over pending jobs whose `needs` are all satisfied, so two runners never claim the same job.
- [x] Run `TEST_DATABASE_URL=... TEST_REDIS_URL=... go test ./internal/ci/` — expect PASS.
- [x] Commit as `feat: add ci scheduler consuming push events idempotently`.

## Evidence

- Task 5: the scheduler consumes stream:git:push with XACK-after-success and a 30s XAUTOCLAIM sweep; run creation is INSERT ... ON CONFLICT DO NOTHING so redelivery is a no-op; ClaimJob uses FOR UPDATE SKIP LOCKED and never returns a job whose needs are unsatisfied.
- Built by a parallel agent in an isolated worktree under red-green TDD, merged to main, and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge): ok ci 7.880s, ok runner 1.059s, ok retention 3.456s, 0 failures. Real PostgreSQL 16, Redis 7 and MinIO in the kw cluster.
- A real flakiness source was found and fixed, not ignored: a blanket TRUNCATE per test raced against other suites sharing the external Postgres. Tests were rescoped to their own org, run and random bucket, with TRUNCATE reserved for the two that genuinely need exclusive access to the job queue.
- A real SECURITY defect was caught by the commit gate and fixed rather than suppressed: the runner executed the repository's workflow command directly on the runner host. A CI job runs whatever the workflow file says, so that command is attacker-controlled by construction. Jobs now run in their own Kubernetes pod built from the job's image, with the clone performed inside the pod; the host path requires NOVAFORGE_ALLOW_LOCAL_EXEC=1 and a runner outside a cluster without it refuses to start. TestLocalExecutionRefusedByDefault and TestPodExecutorCreatesIsolatedPod pin both halves.
- `go build ./...` and `go vet ./...` exit 0.
- Implementing commits: 87bfa97, 92f9da6, 36b23ed, 13d2b92, 4884078, 6c0bc8b, 58a0df0, b98555b, plus the isolation fix in d783e12.
