# work-ci 10: Retention policy

Status: open
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

- [ ] Write the up migration creating `retention_policies` (org_id uuid primary key, log_days int not null default 90 check (log_days >= 0), evidence_days int not null default 0 check (evidence_days >= 0)) and the matching down migration.
- [ ] Write the failing test `internal/retention/policy_test.go`: `func TestDefaultPolicyWhenUnset(t *testing.T)` asserts an org with no row yields `LogDays == 90` and `EvidenceDays == 0`; `func TestSweepDeletesExpiredLogsOnly(t *testing.T)` seeds one sealed log 100 days old and one 10 days old plus a 100-day-old proof record, runs `Sweep`, and asserts the old log object is gone while both the recent log and the proof record remain; `func TestSweepHonoursZeroAsForever(t *testing.T)` asserts `EvidenceDays == 0` deletes no evidence at any age. Run `go test ./internal/retention/` — expect FAIL with "undefined: retention.Sweeper".
- [ ] Implement `Sweep` iterating orgs, deleting sealed log objects and artifact objects older than `LogDays` through `blobstore.Delete` before deleting their rows, and skipping any category whose configured days is zero.
- [ ] Run `TEST_DATABASE_URL=... TEST_S3_ENDPOINT=... go test ./internal/retention/` — expect PASS.
- [ ] Commit as `feat: add per-org retention policy with sweeper`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
