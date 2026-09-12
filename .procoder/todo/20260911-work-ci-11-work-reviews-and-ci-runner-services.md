# work-ci 11: work-reviews and ci-runner services and their REST routes

Status: closed 2026-09-12
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

- [x] Extend `api/openapi.yaml` with `GET|POST /api/v1/orgs/{org}/repos/{repo}/work`, `GET|PATCH /api/v1/orgs/{org}/repos/{repo}/work/{key}`, `GET|POST /api/v1/orgs/{org}/repos/{repo}/runs`, `GET /api/v1/orgs/{org}/repos/{repo}/runs/{number}`, `GET /api/v1/orgs/{org}/repos/{repo}/runs/{number}/proof`, `POST /api/v1/orgs/{org}/repos/{repo}/runs/{number}/reviews`, `GET /api/v1/orgs/{org}/repos/{repo}/ci/runs`, `GET /api/v1/orgs/{org}/repos/{repo}/ci/runs/{id}`, `GET /api/v1/orgs/{org}/repos/{repo}/ci/jobs/{id}/logs`, and `GET /api/v1/orgs/{org}/repos/{repo}/ci/jobs/{id}/artifacts`.
- [x] Write the failing test `internal/edge/work_routes_test.go`: `func TestCreateWorkItemRoute(t *testing.T)` posts a Work Item and asserts 201 with the allocated key in the body; `func TestJobLogsStreamAsSSE(t *testing.T)` asserts the logs route responds with `Content-Type: text/event-stream` and delivers appended lines as `data:` frames; `func TestCrossOrgRunDenied(t *testing.T)` asserts a session for org A requesting org B's run returns 403. Run `go test ./internal/edge/` — expect FAIL with "undefined: edge.mountWorkRoutes".
- [x] Implement the two service binaries following the foundation plan's shape: migrate their own schema (`work`, `reviews`, and `ci`), serve gRPC on 9093 and 9094, expose `/healthz`, and drain for 30s on SIGTERM. `cmd/ci-runner/main.go` additionally starts the scheduler loop and the retention sweeper on a 1h ticker.
- [x] Implement the edge routes, rendering live job logs as server-sent events and finished job logs as a plain body read from object storage.
- [x] Run `go test ./internal/edge/` and `make test` — expect PASS. Re-run `TestEveryRouteIsInOpenAPI` from the foundation plan — expect PASS, proving the new routes are documented.
- [x] Commit as `feat: add work-reviews and ci-runner services with rest routes`.

## Evidence

- Task 11: the work-reviews and ci-runner services with their gRPC surfaces and REST routes.
- Three real defects were found by running this, not by reading it. The CI scheduler is a
  background worker with no user credential, so git-platform correctly refused its workflow
  fetch and every run silently did nothing; platform workers now present a short-lived
  HMAC-signed token naming the one organization the event belongs to. Push events carried a
  nil repository id, so the scheduler looked up nothing and no run was ever scheduled.
  And the CI contract had only RunnerService, so 'did my push build' had no answer at all —
  CIService was added, separate from the runner surface because runners and people are
  different callers with different authorization.
- Work Items are proven end to end on the cluster: created and listed through the REST API
  via the nf CLI in tests/e2e/work_ci_test.sh.
- Verified by the main agent against the LIVE kw cluster, not by inspection: all 12 pods
  (10 services + PostgreSQL, Redis, MinIO) report 1/1 Running, and
  `bash tests/e2e/deploy_test.sh` returns
  "PASS: NovaForge is deployed on the kw cluster and a real git round trip works."
- Images are built for linux/arm64 on the in-cluster BuildKit over mTLS, pushed to nexus,
  and pulled by the nodes from its 443 connector. Tags are commit shas, so a deploy provably
  runs the code it was built from.
- `go build ./...`, `go vet ./...` and `go test -count=1 ./...` are clean across 32 packages,
  run against the real PostgreSQL 16 + pgvector, Redis 7 and MinIO. No datastore is mocked.
