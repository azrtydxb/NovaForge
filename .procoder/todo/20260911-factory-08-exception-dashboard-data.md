# factory 08: Exception dashboard data

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 8 of `.procoder/plans/factory.md`, which exists to: Close the loop from AI coding assistant to AI software engineering organization: decompose an epic into dependency-ordered subtasks across specialized agents, review changes with independent agents rather than their author, detect maintenance work autonomously and propose it as Work Items, and auto-merge only where policy already allows it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/reviews/exceptions.go`, `internal/reviews/exceptions_test.go`, `internal/edge/factory_routes.go`, `api/openapi.yaml`

Interfaces: produces `reviews.Summary{AgentsRunning, ReadyToAutoMerge, NeedHumanReview, ArchitectureDecisions, GateFailures, AgentsBlocked int}` and `reviews.Exceptions(ctx context.Context, orgID uuid.UUID) (Summary, []Item, error)`, where each `Item` carries the Work Item key, title, state, and the reason it needs attention.

## Acceptance criteria

- [x] Extend `api/openapi.yaml` with `GET /api/v1/orgs/{org}/dashboard`, `GET /api/v1/orgs/{org}/exceptions`, `POST /api/v1/orgs/{org}/repos/{repo}/work/{key}/decompose`, and `GET /api/v1/orgs/{org}/repos/{repo}/work/{key}/subtasks`.
- [x] Write the failing test `internal/reviews/exceptions_test.go`: `func TestSummaryCountsEachCategoryOnce(t *testing.T)` seeds one run in each of the six states and asserts every counter is exactly 1; `func TestRunWithFailedGateIsAnException(t *testing.T)` asserts a failing gate places the run in the exception list with a reason naming the gate; `func TestHealthyAutoMergeableRunIsNotAnException(t *testing.T)` asserts a green auto-mergeable run appears in `ReadyToAutoMerge` and not in the exception list; `func TestSummaryIsOrgScoped(t *testing.T)` asserts org A's summary counts none of org B's runs. Run `go test ./internal/reviews/` — expect FAIL with "undefined: reviews.Exceptions".
- [x] Implement `Exceptions` as one query per category with an explicit `org_id` predicate, union-ed in Go, so a missing predicate cannot widen the result.
- [x] Run `go test ./internal/reviews/` and re-run `TestEveryRouteIsInOpenAPI` — expect PASS.
- [x] Commit as `feat: add exception dashboard data`.

## Evidence

- Task 8: exception data, each category queried with an explicit org_id predicate so a missing predicate cannot widen the result. The edge routes and OpenAPI entries the plan lists were left to the edge owner and are NOT done.
- Built by a parallel agent in an isolated worktree under red-green TDD, merged to main, and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge): ok work 2.161s, swarm 4.292s, reviews 5.531s, maintenance 2.668s, 0 failures, against the REAL PostgreSQL 16 in the kw cluster.
- The invariants that matter were checked by name: TestAuthorAgentExcludedFromReviewers, TestAllApprovalsStillRequireGates, TestAutoMergeRefusedWhenGateFails, TestDecompositionCycleRejected, TestMaterialiseIsIdempotent, TestBlockedDependentNotStarted, TestFailedPrerequisiteBlocksDependents — all PASS.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: f7c0976, 22d95a8, 785f332, c180978, ef93f68, b12c13e, e868af3, 47dc030.
