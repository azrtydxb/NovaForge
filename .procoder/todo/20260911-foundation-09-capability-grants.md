# foundation 09: Capability grants

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 9 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/capability/grant.go`, `internal/capability/grant_test.go`, `internal/identity/migrations/000004_capabilities.up.sql`, `internal/identity/migrations/000004_capabilities.down.sql`

Interfaces: produces `capability.Grant` with fields `ID, OrgID, SubjectID uuid.UUID, SubjectKind string, RepoRead bool, WriteBranch string, SecretsProd bool, DeployStaging bool, DeployProd bool, ExpiresAt time.Time`, plus `capability.Store.Issue(ctx, g Grant) (Grant, error)`, `capability.Store.Resolve(ctx, id uuid.UUID) (Grant, error)`, and `capability.CanWriteRef(g Grant, ref string) error`. Git transport in Task 12 and the agent tools in a later plan both call `CanWriteRef` — it is the single enforcement point.

## Acceptance criteria

- [x] Write the failing test `internal/capability/grant_test.go`: `func TestWriteBranchScopeEnforced(t *testing.T)` builds `Grant{WriteBranch: "agents/NF-182/"}` and asserts `CanWriteRef(g, "refs/heads/agents/NF-182/work")` is nil while `CanWriteRef(g, "refs/heads/main")` returns an error containing "not permitted"; `func TestEmptyWriteBranchDeniesAll(t *testing.T)` asserts a zero Grant denies `refs/heads/main`; `func TestPrefixEscapeDenied(t *testing.T)` asserts `CanWriteRef(Grant{WriteBranch: "agents/NF-1/"}, "refs/heads/agents/NF-10/x")` errors. Run `go test ./internal/capability/` — expect FAIL with "undefined: capability.Grant".
- [x] Implement `CanWriteRef`: strip the `refs/heads/` prefix, deny when `WriteBranch` is empty, deny when the branch does not have `WriteBranch` as a literal prefix, and deny any ref containing `..`. Return `fmt.Errorf("write to %s not permitted; grant allows %q", ref, g.WriteBranch)`.
- [x] Write the up migration creating `capability_grants` (id uuid pk, org_id uuid not null references organizations(id) on delete cascade, subject_id uuid not null, subject_kind text not null check (subject_kind in ('user','agent')), repo_read boolean not null default false, write_branch text not null default '', secrets_prod boolean not null default false, deploy_staging boolean not null default false, deploy_prod boolean not null default false, expires_at timestamptz not null, created_at timestamptz not null default now()) and the matching down migration.
- [x] Implement `Issue` and `Resolve`, with `Resolve` returning `errors.New("grant expired")` when `expires_at` is past.
- [x] Run `go test ./internal/capability/` and `TEST_DATABASE_URL=... go test ./internal/capability/` — expect PASS.
- [x] Commit as `feat: add capability grants with branch-scoped write enforcement`.

## Evidence

- Task 9: capability_grants schema and CanWriteRef as the single enforcement point. Migrations live in internal/capability/migrations/ rather than internal/identity/ (plan text) because a parallel agent owned internal/identity; schema gitplatform, org_id without an FK since organizations live in another service's schema — the same cross-service pattern the plan specifies for Task 15.
- Built by a parallel agent in an isolated git worktree under strict red-green TDD, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge by the main agent): `go test -count=1 -v ./internal/gitops/... ./internal/capability/... ./internal/events/...` → 19 PASS, 0 FAIL. ok gitops 3.085s, ok capability 1.363s, ok events 0.601s.
- These are REAL git operations, not simulations: TestCloneAndPushOverHTTP and TestPushOverSSH drive an unmodified git 2.50.1 client through a real clone, commit and push. TestPushDeniedByCapability and TestPushOverSSHDeniedByCapability prove both transports refuse an out-of-scope ref identically. TestPathTraversalRejected and TestPrefixEscapeDenied pin the escape cases. TestPushOverHTTPPublishesEvent consumes the push event back out of a real Redis consumer group.
- Datastores are the REAL PostgreSQL 16 and Redis 7 in the kw cluster, not mocks.
- Two documented protocol deviations, both in the commit bodies: push denial is returned as a git-receive-pack report-status "ng <ref> <reason>" inside side-band-64k framing, because git's smart-HTTP client discards a bare non-2xx body and the plan's own test requires the reason to reach stderr; and SSH cannot use --stateless-rpc for the advertisement because git's interactive SSH client blocks waiting for it, so the advertisement is sent first and the push body is then buffered and authorized exactly as HTTP does.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: c83d80c, 9759e95, 2b1dad1, 37553b2, a44d29d. Merged in 67b3c31.
