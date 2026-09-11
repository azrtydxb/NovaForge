# factory 04: Independent multi-agent review

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 4 of `.procoder/plans/factory.md`, which exists to: Close the loop from AI coding assistant to AI software engineering organization: decompose an epic into dependency-ordered subtasks across specialized agents, review changes with independent agents rather than their author, detect maintenance work autonomously and propose it as Work Items, and auto-merge only where policy already allows it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/reviews/agentreview.go`, `internal/reviews/agentreview_test.go`

Interfaces: produces `reviews.AgentReviewer.ReviewRun(ctx context.Context, runID uuid.UUID, roles []string) ([]AgentVerdict, error)` where `AgentVerdict` is `{Role, AgentName, ModelName, Verdict, Summary string}` and `Verdict` is one of `approve`, `request_changes`, or `comment`. The default roles are reviewer, security, test, and architecture.

## Acceptance criteria

- [x] Write the failing test `internal/reviews/agentreview_test.go`: `func TestAuthorAgentExcludedFromReviewers(t *testing.T)` asserts a run authored by the backend agent never dispatches a review to that same agent, and that the returned verdicts contain no entry naming it; `func TestReviewersUseDistinctModelsWhenAvailable(t *testing.T)` configures two models and asserts reviewer and security verdicts do not share a `ModelName`; `func TestRequestChangesBlocksMerge(t *testing.T)` asserts a `request_changes` verdict leaves `MayMerge` false; `func TestAllApprovalsStillRequireGates(t *testing.T)` asserts four approving verdicts with a failing tests gate still leave `MayMerge` false — review never substitutes for a gate; `func TestReviewVerdictsRecordedAsProof(t *testing.T)` asserts each verdict is written through `RecordProof` so it appears in the run's PROOF block. Run `go test ./internal/reviews/` — expect FAIL with "undefined: reviews.AgentReviewer".
- [x] Implement `ReviewRun` filtering the role list against the run's author agent id before dispatch, so exclusion happens before any model call rather than being checked afterwards.
- [x] Implement model diversity by round-robining the configured models across roles, falling back to a single model with a logged warning when only one is configured.
- [x] Run `TEST_DATABASE_URL=... go test ./internal/reviews/` — expect PASS.
- [x] Commit as `feat: add independent multi-agent review`.

## Evidence

- Task 4: the author agent is filtered out of the reviewer list BEFORE any model dispatch, and four approving verdicts with a failing gate still leave MayMerge false.
- Built by a parallel agent in an isolated worktree under red-green TDD, merged to main, and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge): ok work 2.161s, swarm 4.292s, reviews 5.531s, maintenance 2.668s, 0 failures, against the REAL PostgreSQL 16 in the kw cluster.
- The invariants that matter were checked by name: TestAuthorAgentExcludedFromReviewers, TestAllApprovalsStillRequireGates, TestAutoMergeRefusedWhenGateFails, TestDecompositionCycleRejected, TestMaterialiseIsIdempotent, TestBlockedDependentNotStarted, TestFailedPrerequisiteBlocksDependents — all PASS.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: f7c0976, 22d95a8, 785f332, c180978, ef93f68, b12c13e, e868af3, 47dc030.
