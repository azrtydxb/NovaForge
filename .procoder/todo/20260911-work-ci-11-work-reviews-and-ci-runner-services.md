# work-ci 11: work-reviews and ci-runner services and their REST routes

Status: open
Created: 2026-09-11

## Description

Plan step 11 of `.procoder/plans/work-ci.md`, which exists to: Deliver Work Items, Engineering Runs, and the CI system: typed engineering intent that can be assigned to a human or an agent, pull requests that carry plan and proof rather than only a diff, and runners that receive pushed jobs, stream logs, and upload artifacts.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `proto/work/v1/work.proto`, `proto/reviews/v1/reviews.proto`, `cmd/work-reviews/main.go`, `cmd/ci-runner/main.go`, `internal/edge/work_routes.go`, `internal/edge/ci_routes.go`, `internal/edge/work_routes_test.go`, `api/openapi.yaml`, `Dockerfile.work-reviews`, `Dockerfile.ci-runner`

Interfaces: produces `novaforge.work.v1.WorkService` (`CreateItem`, `GetItem`, `ListItems`, `AssignItem`), `novaforge.reviews.v1.ReviewsService` (`CreateRun`, `GetRun`, `ListRuns`, `AddPlanStep`, `RecordProof`, `SubmitReview`, `AddComment`), and `novaforge.ci.v1.CIService` (`ListRuns`, `GetRun`, `GetJobLogs`, `ListArtifacts`, `DispatchWorkflow`). The edge mounts these under the repository path established by the foundation plan.

## Acceptance criteria

- [ ] Extend `api/openapi.yaml` with `GET|POST /api/v1/orgs/{org}/repos/{repo}/work`, `GET|PATCH /api/v1/orgs/{org}/repos/{repo}/work/{key}`, `GET|POST /api/v1/orgs/{org}/repos/{repo}/runs`, `GET /api/v1/orgs/{org}/repos/{repo}/runs/{number}`, `GET /api/v1/orgs/{org}/repos/{repo}/runs/{number}/proof`, `POST /api/v1/orgs/{org}/repos/{repo}/runs/{number}/reviews`, `GET /api/v1/orgs/{org}/repos/{repo}/ci/runs`, `GET /api/v1/orgs/{org}/repos/{repo}/ci/runs/{id}`, `GET /api/v1/orgs/{org}/repos/{repo}/ci/jobs/{id}/logs`, and `GET /api/v1/orgs/{org}/repos/{repo}/ci/jobs/{id}/artifacts`.
- [ ] Write the failing test `internal/edge/work_routes_test.go`: `func TestCreateWorkItemRoute(t *testing.T)` posts a Work Item and asserts 201 with the allocated key in the body; `func TestJobLogsStreamAsSSE(t *testing.T)` asserts the logs route responds with `Content-Type: text/event-stream` and delivers appended lines as `data:` frames; `func TestCrossOrgRunDenied(t *testing.T)` asserts a session for org A requesting org B's run returns 403. Run `go test ./internal/edge/` — expect FAIL with "undefined: edge.mountWorkRoutes".
- [ ] Implement the two service binaries following the foundation plan's shape: migrate their own schema (`work`, `reviews`, and `ci`), serve gRPC on 9093 and 9094, expose `/healthz`, and drain for 30s on SIGTERM. `cmd/ci-runner/main.go` additionally starts the scheduler loop and the retention sweeper on a 1h ticker.
- [ ] Implement the edge routes, rendering live job logs as server-sent events and finished job logs as a plain body read from object storage.
- [ ] Run `go test ./internal/edge/` and `make test` — expect PASS. Re-run `TestEveryRouteIsInOpenAPI` from the foundation plan — expect PASS, proving the new routes are documented.
- [ ] Commit as `feat: add work-reviews and ci-runner services with rest routes`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
