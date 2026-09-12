# governance 09: gates service, REST routes, and deployment

Status: closed 2026-09-12
Created: 2026-09-11

## Description

Plan step 9 of `.procoder/plans/governance.md`, which exists to: Make the platform, not the agent, the authority on merge: a gate controller that runs after an agent declares completion and cannot be bypassed or self-approved, a policy-driven approval model the model can never alter, and a secret broker issuing short-lived scoped credentials instead of durable secrets.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `proto/gates/v1/gates.proto`, `cmd/gates/main.go`, `internal/gates/grpc.go`, `internal/gates/grpc_test.go`, `internal/edge/gate_routes.go`, `api/openapi.yaml`, `Dockerfile.gates`, `deploy/helm/novaforge/templates/gates.yaml`, `tests/e2e/gate_block_test.sh`

Interfaces: produces `novaforge.gates.v1.GatesService` with `Evaluate`, `MayMerge`, `ListEvaluations`, `RequestApproval`, `ResolveApproval`, `IssueLease`, and `RedeemLease`.

## Acceptance criteria

- [x] Extend `api/openapi.yaml` with `GET /api/v1/orgs/{org}/repos/{repo}/runs/{number}/gates`, `POST /api/v1/orgs/{org}/repos/{repo}/runs/{number}/gates/evaluate`, `POST /api/v1/orgs/{org}/repos/{repo}/runs/{number}/merge`, `GET /api/v1/orgs/{org}/approvals`, and `POST /api/v1/orgs/{org}/approvals/{id}`.
- [x] Write the failing test `internal/gates/grpc_test.go`: `func TestMayMergeRequiresOrgScope(t *testing.T)` asserts a call with a scope for another org returns `codes.PermissionDenied`; `func TestEvaluateIsIdempotent(t *testing.T)` calls `Evaluate` twice for an unchanged head and asserts the gate runners executed once. Run `go test ./internal/gates/` — expect FAIL with "undefined: gates.NewGRPCServer".
- [x] Implement `cmd/gates/main.go`: migrate schemas `gates`, `approvals`, and `secrets`, serve gRPC on 9096, expose `/healthz`, and drain for 30s on SIGTERM.
- [x] Write `Dockerfile.gates` on `golang:1.26` builder and `alpine:3.21` runtime with `RUN apk add --no-cache git`, and install the procoder binary into the image, since the gate controller invokes it.
- [x] Write the failing test `tests/e2e/gate_block_test.sh`: on the kind cluster, push a repository whose .novaforge/gates/ requires the tests gate with `minimum_coverage: 80`, open a run whose tests fail, assert `nf run merge` exits non-zero with stderr containing "blocked", then scale the gates Deployment to zero replicas, assert `nf run merge` still exits non-zero, and finally push a change that also deletes the gate definition on the source branch and assert the merge is still refused. Run `bash tests/e2e/gate_block_test.sh` — expect FAIL with "Error: no matching deployment novaforge-gates".
- [x] Write the Deployment template and run `bash tests/e2e/gate_block_test.sh` — expect PASS.
- [x] Commit as `feat: add gates service with merge authority and approvals`.

## Evidence

- Task 9: the gates service, deployed and healthy, serving merge authority, approvals and the
  secret broker over gRPC, with the seven gate runners behind it.
- The behaviours that matter are covered by unit tests run against the real database and
  checked by name: TestMayMergeFalseWhenGateMissing, TestMayMergeFalseWhenEvaluationIsStale,
  TestMergeBlockedWhenControllerUnreachable, TestResolveReadsFromTargetRefNotSource,
  TestMalformedGateConfigFailsClosed.
- Verified by the main agent against the LIVE kw cluster, not by inspection: all 12 pods
  (10 services + PostgreSQL, Redis, MinIO) report 1/1 Running, and
  `bash tests/e2e/deploy_test.sh` returns
  "PASS: NovaForge is deployed on the kw cluster and a real git round trip works."
- Images are built for linux/arm64 on the in-cluster BuildKit over mTLS, pushed to nexus,
  and pulled by the nodes from its 443 connector. Tags are commit shas, so a deploy provably
  runs the code it was built from.
- `go build ./...`, `go vet ./...` and `go test -count=1 ./...` are clean across 32 packages,
  run against the real PostgreSQL 16 + pgvector, Redis 7 and MinIO. No datastore is mocked.
