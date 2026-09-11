# foundation 16: OpenAPI contract and REST edge

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 16 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `api/openapi.yaml`, `cmd/edge/main.go`, `internal/edge/router.go`, `internal/edge/middleware.go`, `internal/edge/router_test.go`, `internal/edge/coverage_test.go`

Interfaces: produces `edge.NewRouter(cfg Config) http.Handler` where `Config` carries `Identity identityv1.IdentityServiceClient`, `Git gitv1.GitServiceClient`, and `Redis *redis.Client`. Routes are mounted under `/api/v1` and every route has an `operationId` in `api/openapi.yaml`.

## Acceptance criteria

- [x] Write `api/openapi.yaml` as OpenAPI 3.1 describing every route this task mounts: `POST /api/v1/auth/register`, `POST /api/v1/auth/login`, `POST /api/v1/auth/logout`, `GET /api/v1/user`, `GET|POST /api/v1/user/tokens`, `DELETE /api/v1/user/tokens/{id}`, `GET|POST /api/v1/user/ssh-keys`, `DELETE /api/v1/user/ssh-keys/{id}`, `POST /api/v1/user/2fa/setup`, `POST /api/v1/user/2fa/verify`, `GET|POST /api/v1/orgs`, `GET /api/v1/orgs/{org}`, `GET|POST /api/v1/orgs/{org}/members`, `GET|POST /api/v1/orgs/{org}/repos`, `GET|DELETE /api/v1/orgs/{org}/repos/{repo}`, `GET /api/v1/orgs/{org}/repos/{repo}/branches`, `GET /api/v1/orgs/{org}/repos/{repo}/tags`, `GET /api/v1/orgs/{org}/repos/{repo}/commits/{ref}`, `GET /api/v1/orgs/{org}/repos/{repo}/tree/{ref}/{path}`, `GET /api/v1/orgs/{org}/repos/{repo}/blob/{ref}/{path}`, and `GET /api/v1/orgs/{org}/repos/{repo}/diff`.
- [x] Write the failing test `internal/edge/coverage_test.go`: `func TestEveryRouteIsInOpenAPI(t *testing.T)` walks the chi router with `chi.Walk`, collects `method + pattern`, parses `api/openapi.yaml`, and fails listing any route absent from the document — and any documented path absent from the router. Run `go test ./internal/edge/` — expect FAIL with "undefined: edge.NewRouter".
- [x] Write the failing test `internal/edge/router_test.go`: `func TestUnauthenticatedRejected(t *testing.T)` asserts `GET /api/v1/user` without credentials returns 401; `func TestCrossOrgRepoDenied(t *testing.T)` asserts a session for org A requesting org B's repository returns 403.
- [x] Implement `middleware.go`: authentication accepting either a session cookie or `Authorization: Bearer nf_...`, resolving through `IdentityService` and installing an `authz.Scope` in the request context; plus a Redis fixed-window rate limiter keyed on the resolved actor.
- [x] Implement `router.go` mounting every documented route and translating gRPC status codes to HTTP (`codes.PermissionDenied` to 403, `codes.NotFound` to 404, `codes.Unauthenticated` to 401).
- [x] Run `go test ./internal/edge/` — expect PASS.
- [x] Commit as `feat: add openapi contract and rest edge with coverage test`.

## Evidence

- Task 16: the REST edge. The router and api/openapi.yaml are generated from one route table, so a route cannot exist without a contract entry, and TestEveryRouteIsInOpenAPI fails the build if the checked-in document is hand-edited.
- Green (unit): `go test -count=1 ./...` passes across every package, against the REAL PostgreSQL 16 + pgvector, Redis 7 and MinIO running in the kw cluster.
- Green (CLUSTER ACCEPTANCE, the evidence that matters): `bash tests/e2e/deploy_test.sh` against the live 8-node ARM64 k3s cluster returned:
  "PASS: NovaForge is deployed on the kw cluster and a real git round trip works."
  Specifically: all six deployments ready; the edge answering /healthz at 192.168.10.128; register, login, org create and repo create through the REST API via the nf CLI; an UNMODIFIED git client cloning over HTTPS from 192.168.10.123:8081, committing and pushing (commit 5f8a6130fd49e55a4a3bc121d6785ef2eec1f89f); and that commit and its branch read back through the REST API.
- Images were built for linux/arm64 on the in-cluster BuildKit over mTLS, pushed to nexus, and pulled by the nodes. No local Docker daemon was involved.
- Four real defects were found by running this against the cluster rather than by inspection, each fixed with a test: the edge resolved a bearer only as a PAT so CLI calls failed; the edge called services anonymously after authenticating; a credential carried no organization so every org-scoped service refused; and the org lookup queried an unqualified table that no test covered.
- `go build ./...` and `go vet ./...` exit 0.
