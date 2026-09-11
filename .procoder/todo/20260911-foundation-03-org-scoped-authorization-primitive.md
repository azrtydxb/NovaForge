# foundation 03: Org-scoped authorization primitive

Status: open
Created: 2026-09-11

## Description

Plan step 3 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/authz/scope.go`, `internal/authz/scope_test.go`

Interfaces: produces `authz.Scope{OrgID uuid.UUID, ActorID uuid.UUID, ActorKind string}`, `authz.FromContext(ctx context.Context) (Scope, error)`, `authz.WithScope(ctx, Scope) context.Context`, and `authz.RequireOrg(ctx context.Context, orgID uuid.UUID) error`. Every query in every later task derives its org predicate from this and never accepts an org id from the request body.

## Acceptance criteria

- [ ] Write the failing test `internal/authz/scope_test.go`: `func TestRequireOrgRejectsCrossOrg(t *testing.T)` builds `ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: orgA, ActorID: user, ActorKind: "user"})` and asserts `authz.RequireOrg(ctx, orgB)` returns an error whose message contains "cross-org"; plus `func TestFromContextEmpty(t *testing.T)` asserting `authz.FromContext(context.Background())` errors rather than returning a zero Scope. Run `go test ./internal/authz/` — expect FAIL with "undefined: authz.WithScope".
- [ ] Implement `scope.go` with an unexported context key type (`type ctxKey struct{}`) so no other package can collide or forge the value.
- [ ] Implement `RequireOrg` returning `fmt.Errorf("cross-org access denied: scope org %s, requested %s", s.OrgID, orgID)` on mismatch and `nil` on match.
- [ ] Run `go test ./internal/authz/` — expect PASS.
- [ ] Commit as `feat: add org-scoped authorization primitive`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
