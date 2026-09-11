# factory 08: Exception dashboard data

Status: open
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

- [ ] Extend `api/openapi.yaml` with `GET /api/v1/orgs/{org}/dashboard`, `GET /api/v1/orgs/{org}/exceptions`, `POST /api/v1/orgs/{org}/repos/{repo}/work/{key}/decompose`, and `GET /api/v1/orgs/{org}/repos/{repo}/work/{key}/subtasks`.
- [ ] Write the failing test `internal/reviews/exceptions_test.go`: `func TestSummaryCountsEachCategoryOnce(t *testing.T)` seeds one run in each of the six states and asserts every counter is exactly 1; `func TestRunWithFailedGateIsAnException(t *testing.T)` asserts a failing gate places the run in the exception list with a reason naming the gate; `func TestHealthyAutoMergeableRunIsNotAnException(t *testing.T)` asserts a green auto-mergeable run appears in `ReadyToAutoMerge` and not in the exception list; `func TestSummaryIsOrgScoped(t *testing.T)` asserts org A's summary counts none of org B's runs. Run `go test ./internal/reviews/` — expect FAIL with "undefined: reviews.Exceptions".
- [ ] Implement `Exceptions` as one query per category with an explicit `org_id` predicate, union-ed in Go, so a missing predicate cannot widen the result.
- [ ] Run `go test ./internal/reviews/` and re-run `TestEveryRouteIsInOpenAPI` — expect PASS.
- [ ] Commit as `feat: add exception dashboard data`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
