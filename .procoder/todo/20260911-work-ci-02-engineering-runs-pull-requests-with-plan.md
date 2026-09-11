# work-ci 02: Engineering Runs — pull requests with plan and proof

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 2 of `.procoder/plans/work-ci.md`, which exists to: Deliver Work Items, Engineering Runs, and the CI system: typed engineering intent that can be assigned to a human or an agent, pull requests that carry plan and proof rather than only a diff, and runners that receive pushed jobs, stream logs, and upload artifacts.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/reviews/migrations/000001_reviews.up.sql`, `internal/reviews/migrations/000001_reviews.down.sql`, `internal/reviews/store.go`, `internal/reviews/store_test.go`

Interfaces: produces `reviews.Run` with fields `ID, OrgID, RepoID, WorkItemID uuid.UUID, Number int, Title string, SourceRef, TargetRef string, State string, AuthorID uuid.UUID, AuthorKind string, AgentName, ModelName string, CreatedAt time.Time`, `reviews.PlanStep{RunID uuid.UUID, Ordinal int, Text, State string}`, `reviews.ProofRecord{RunID uuid.UUID, Gate string, Status string, Detail string, RecordedAt time.Time}`, and `reviews.Store` with `CreateRun`, `GetRun`, `ListRuns`, `AddPlanStep`, `SetPlanStepState`, `RecordProof`, `ListProof`, `AddComment`, and `SubmitReview(ctx, runID, reviewerID uuid.UUID, reviewerKind, verdict string) error`.

## Acceptance criteria

- [x] Write the failing test `internal/reviews/store_test.go`: `func TestCreateRunAllocatesNumber(t *testing.T)` asserts two runs in one repository get numbers 1 and 2; `func TestProofRecordsAccumulate(t *testing.T)` records proof for gates `tests` and `security` and asserts `ListProof` returns both with their statuses; `func TestAuthorCannotBeSoleApprover(t *testing.T)` creates a run authored by agent A, calls `SubmitReview` with reviewer A and verdict `approve`, and asserts it returns an error containing "author cannot approve"; `func TestSecondReviewerApproves(t *testing.T)` asserts reviewer B approving the same run succeeds. Run `go test ./internal/reviews/` — expect FAIL with "undefined: reviews.Store".
- [x] Write the up migration creating `runs` (id uuid pk, org_id uuid not null, repo_id uuid not null, work_item_id uuid, number int not null, title text not null, source_ref text not null, target_ref text not null, state text not null default 'open' check (state in ('open','merged','closed')), author_id uuid not null, author_kind text not null check (author_kind in ('user','agent')), agent_name text, model_name text, created_at timestamptz not null default now(), unique (repo_id, number)), plus `run_plan_steps`, `run_proof` (unique (run_id, gate)), `run_comments`, and `run_reviews` (unique (run_id, reviewer_id)); and the matching down migration.
- [x] Implement `SubmitReview` rejecting self-approval with `fmt.Errorf("author cannot approve their own run %s", runID)` whenever `reviewer_id = author_id`, regardless of `author_kind` — this is the enforcement point for the spec's independent-review requirement.
- [x] Implement `RecordProof` as an upsert on `(run_id, gate)` so an at-least-once redelivery of the same gate result does not duplicate rows.
- [x] Run `TEST_DATABASE_URL=... go test ./internal/reviews/` — expect PASS.
- [x] Commit as `feat: add engineering runs with plan steps, proof records, and review rules`.

## Evidence

- Task 2: runs, plan steps, proof records, comments and reviews. RecordProof upserts on (run_id, gate) so redelivery cannot duplicate; SubmitReview rejects self-approval before author_kind is consulted.
- Built by a parallel agent in an isolated worktree under red-green TDD, merged to main, and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge, re-run just now on the merged tree): ok internal/work, ok internal/reviews, ok internal/ci, 0 failures, against the REAL PostgreSQL 16 in the kw cluster.
- `go build ./...` and `go vet ./...` exit 0.
- Implementing commits: 9444bd2, c1290c2, 8552d8e, 6de4be6.
