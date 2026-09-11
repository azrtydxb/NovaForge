# foundation 09: Capability grants

Status: open
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

- [ ] Write the failing test `internal/capability/grant_test.go`: `func TestWriteBranchScopeEnforced(t *testing.T)` builds `Grant{WriteBranch: "agents/NF-182/"}` and asserts `CanWriteRef(g, "refs/heads/agents/NF-182/work")` is nil while `CanWriteRef(g, "refs/heads/main")` returns an error containing "not permitted"; `func TestEmptyWriteBranchDeniesAll(t *testing.T)` asserts a zero Grant denies `refs/heads/main`; `func TestPrefixEscapeDenied(t *testing.T)` asserts `CanWriteRef(Grant{WriteBranch: "agents/NF-1/"}, "refs/heads/agents/NF-10/x")` errors. Run `go test ./internal/capability/` — expect FAIL with "undefined: capability.Grant".
- [ ] Implement `CanWriteRef`: strip the `refs/heads/` prefix, deny when `WriteBranch` is empty, deny when the branch does not have `WriteBranch` as a literal prefix, and deny any ref containing `..`. Return `fmt.Errorf("write to %s not permitted; grant allows %q", ref, g.WriteBranch)`.
- [ ] Write the up migration creating `capability_grants` (id uuid pk, org_id uuid not null references organizations(id) on delete cascade, subject_id uuid not null, subject_kind text not null check (subject_kind in ('user','agent')), repo_read boolean not null default false, write_branch text not null default '', secrets_prod boolean not null default false, deploy_staging boolean not null default false, deploy_prod boolean not null default false, expires_at timestamptz not null, created_at timestamptz not null default now()) and the matching down migration.
- [ ] Implement `Issue` and `Resolve`, with `Resolve` returning `errors.New("grant expired")` when `expires_at` is past.
- [ ] Run `go test ./internal/capability/` and `TEST_DATABASE_URL=... go test ./internal/capability/` — expect PASS.
- [ ] Commit as `feat: add capability grants with branch-scoped write enforcement`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
