# work-ci 05: CI schema, scheduler, and push consumer

Status: open
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

- [ ] Write the up migration creating `workflow_runs` (id uuid pk, org_id uuid not null, repo_id uuid not null, commit_sha text not null, ref text not null, status text not null default 'queued' check (status in ('queued','running','success','failure','cancelled')), created_at timestamptz not null default now(), unique (repo_id, commit_sha, ref)), `workflow_jobs` (id uuid pk, run_id uuid not null references workflow_runs(id) on delete cascade, name text not null, needs text[] not null default '{}', run_cmd text, agent_role text, image text, status text not null default 'pending', runner_id uuid, started_at timestamptz, finished_at timestamptz, unique (run_id, name)), and `runners` (id uuid pk, org_id uuid not null, name text not null, labels text[] not null default '{}', token_hash bytea not null unique, last_seen_at timestamptz); plus the down migration.
- [ ] Write the failing test `internal/ci/scheduler_test.go`: `func TestPushSchedulesRun(t *testing.T)` publishes a `PushEvent` to the stream with a stubbed git client returning a workflow with one job, runs the scheduler for one iteration, and asserts a `workflow_runs` row exists with that commit SHA; `func TestDuplicatePushIsIdempotent(t *testing.T)` publishes the identical event twice and asserts exactly one run row exists — proving the handler tolerates at-least-once redelivery; `func TestJobBlockedUntilNeedsSucceed(t *testing.T)` asserts `ClaimJob` never returns a job whose `needs` are not all `success`; `func TestMissingWorkflowSkipsSilently(t *testing.T)` asserts a push to a repository with no `.novaforge/workflow.yaml` creates no run and returns no error. Run `go test ./internal/ci/` — expect FAIL with "undefined: ci.Scheduler".
- [ ] Implement the scheduler loop: `events.EnsureGroup`, then `XREADGROUP` with `Block` of 5s and `Count` of 10, `XACK` only after the handler returns nil, and an `XAUTOCLAIM` pass every 30s reclaiming messages idle longer than 60s.
- [ ] Implement idempotency by making run creation an `INSERT ... ON CONFLICT (repo_id, commit_sha, ref) DO NOTHING RETURNING id` and treating a zero-row result as "already scheduled, nothing to do".
- [ ] Implement `ClaimJob` as `SELECT ... FOR UPDATE SKIP LOCKED` over pending jobs whose `needs` are all satisfied, so two runners never claim the same job.
- [ ] Run `TEST_DATABASE_URL=... TEST_REDIS_URL=... go test ./internal/ci/` — expect PASS.
- [ ] Commit as `feat: add ci scheduler consuming push events idempotently`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
