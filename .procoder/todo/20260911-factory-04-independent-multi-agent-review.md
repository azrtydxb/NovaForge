# factory 04: Independent multi-agent review

Status: open
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

- [ ] Write the failing test `internal/reviews/agentreview_test.go`: `func TestAuthorAgentExcludedFromReviewers(t *testing.T)` asserts a run authored by the backend agent never dispatches a review to that same agent, and that the returned verdicts contain no entry naming it; `func TestReviewersUseDistinctModelsWhenAvailable(t *testing.T)` configures two models and asserts reviewer and security verdicts do not share a `ModelName`; `func TestRequestChangesBlocksMerge(t *testing.T)` asserts a `request_changes` verdict leaves `MayMerge` false; `func TestAllApprovalsStillRequireGates(t *testing.T)` asserts four approving verdicts with a failing tests gate still leave `MayMerge` false — review never substitutes for a gate; `func TestReviewVerdictsRecordedAsProof(t *testing.T)` asserts each verdict is written through `RecordProof` so it appears in the run's PROOF block. Run `go test ./internal/reviews/` — expect FAIL with "undefined: reviews.AgentReviewer".
- [ ] Implement `ReviewRun` filtering the role list against the run's author agent id before dispatch, so exclusion happens before any model call rather than being checked afterwards.
- [ ] Implement model diversity by round-robining the configured models across roles, falling back to a single model with a logged warning when only one is configured.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/reviews/` — expect PASS.
- [ ] Commit as `feat: add independent multi-agent review`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
