# foundation 15: git-platform service

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 15 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `proto/git/v1/git.proto`, `cmd/git-platform/main.go`, `internal/gitops/grpc.go`, `internal/gitops/grpc_test.go`, `internal/gitops/migrations/000001_git.up.sql`, `internal/gitops/migrations/000001_git.down.sql`

Interfaces: produces `novaforge.git.v1.GitService` with RPCs `CreateRepo`, `GetRepo`, `ListRepos`, `ListBranches`, `ListTags`, `ListCommits`, `GetTree`, `GetBlob`, and `GetDiff`. It calls `IdentityService.ResolveToken` and `ResolveFingerprint` for authentication and `IssueGrant`-derived grants for authorization; it never reads the identity schema.

## Acceptance criteria

- [x] Write the up migration creating `repositories` (id uuid pk, org_id uuid not null, name citext not null, default_branch text not null default 'main', created_at timestamptz not null default now(), unique (org_id, name)) and the matching down migration. The org_id column is not a foreign key because organizations live in another service's schema; referential integrity across services is the caller's responsibility.
- [x] Write the failing test `internal/gitops/grpc_test.go`: `func TestCreateRepoInitialisesBareRepo(t *testing.T)` calls `CreateRepo` and asserts the directory `<root>/<orgID>/<name>.git/HEAD` exists; `func TestListReposIsOrgScoped(t *testing.T)` creates repos in two orgs and asserts a scope for org A never sees org B's repository; `func TestGetBlobUnknownPath(t *testing.T)` asserts `codes.NotFound`. Run `go test ./internal/gitops/` — expect FAIL with "undefined: gitops.NewGRPCServer".
- [x] Implement `grpc.go` deriving every query's org predicate from `authz.FromContext` and never from the request message, returning `codes.PermissionDenied` when `authz.RequireOrg` fails.
- [x] Implement `cmd/git-platform/main.go`: assert `version.Require("git","2.40.0")` at startup and exit non-zero on failure, migrate schema `git`, mount the repository root from `GIT_DATA_DIR`, serve gRPC on 9092, the smart-HTTP handler on 8081, and the SSH server on 2222.
- [x] Run `go test ./internal/gitops/` — expect PASS.
- [x] Commit as `feat: add git-platform service with grpc, http, and ssh surfaces`.

## Evidence

- Task 15: the git-platform service serving gRPC, smart-HTTP and SSH together, asserting the git binary version at startup and migrating its own schema.
- Green (unit): `go test -count=1 ./...` passes across every package, against the REAL PostgreSQL 16 + pgvector, Redis 7 and MinIO running in the kw cluster.
- Green (CLUSTER ACCEPTANCE, the evidence that matters): `bash tests/e2e/deploy_test.sh` against the live 8-node ARM64 k3s cluster returned:
  "PASS: NovaForge is deployed on the kw cluster and a real git round trip works."
  Specifically: all six deployments ready; the edge answering /healthz at 192.168.10.128; register, login, org create and repo create through the REST API via the nf CLI; an UNMODIFIED git client cloning over HTTPS from 192.168.10.123:8081, committing and pushing (commit 5f8a6130fd49e55a4a3bc121d6785ef2eec1f89f); and that commit and its branch read back through the REST API.
- Images were built for linux/arm64 on the in-cluster BuildKit over mTLS, pushed to nexus, and pulled by the nodes. No local Docker daemon was involved.
- Four real defects were found by running this against the cluster rather than by inspection, each fixed with a test: the edge resolved a bearer only as a PAT so CLI calls failed; the edge called services anonymously after authenticating; a credential carried no organization so every org-scoped service refused; and the org lookup queried an unqualified table that no test covered.
- `go build ./...` and `go vet ./...` exit 0.
