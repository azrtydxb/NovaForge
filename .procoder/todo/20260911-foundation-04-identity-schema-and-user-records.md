# foundation 04: Identity schema and user records

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 4 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/identity/migrations/000001_identity.up.sql`, `internal/identity/migrations/000001_identity.down.sql`, `internal/identity/store.go`, `internal/identity/store_test.go`

Interfaces: produces `identity.Store` with `CreateUser(ctx, email, username, passwordHash string) (User, error)`, `UserByUsername(ctx, username string) (User, error)`, `CreateOrg(ctx, name string, ownerID uuid.UUID) (Org, error)`, and `AddOrgMember(ctx, orgID, userID uuid.UUID, role string) error`. `User` has fields `ID uuid.UUID, Email, Username, PasswordHash string, TOTPSecret sql.NullString, CreatedAt time.Time`.

## Acceptance criteria

- [x] Write the failing test `internal/identity/store_test.go`: `func TestCreateUserAndLookup(t *testing.T)` creates a user and asserts `UserByUsername` returns the same ID; `func TestDuplicateUsernameRejected(t *testing.T)` asserts a second create with the same username returns an error containing "username". Run `go test ./internal/identity/` — expect FAIL with "undefined: identity.Store".
- [x] Write the up migration creating `users` (id uuid pk, email citext unique not null, username citext unique not null, password_hash text not null, totp_secret text, created_at timestamptz not null default now()), `organizations` (id uuid pk, name citext unique not null, created_at timestamptz not null default now()), and `org_members` (org_id uuid not null references organizations(id) on delete cascade, user_id uuid not null references users(id) on delete cascade, role text not null check (role in ('owner','admin','member')), primary key (org_id, user_id)). Enable `citext` first with `CREATE EXTENSION IF NOT EXISTS citext;`.
- [x] Write the matching down migration dropping the three tables in reverse dependency order.
- [x] Implement `Store` over `*pgxpool.Pool`, mapping the unique-violation SQLSTATE `23505` to `fmt.Errorf("username %q already taken", username)`.
- [x] Run `TEST_DATABASE_URL=... go test ./internal/identity/` — expect PASS.
- [x] Commit as `feat: add identity schema with users, orgs, and memberships`.

## Evidence

- Task 4: users, organizations, org_members schema with citext unique constraints and role check. Store maps SQLSTATE 23505 to a username-taken error.
- Built by a parallel agent in an isolated git worktree, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent after the merge — an agent's report is a claim, not evidence.
- Red-green was followed per task by the implementing agent; the merged result was re-run from a clean checkout.
- Green (verified post-merge by the main agent): `go test ./internal/identity/ -count=1 -v` → 13 PASS, 0 FAIL, ok github.com/novaforge/novaforge/internal/identity 2.544s. Run against the REAL PostgreSQL 16 + pgvector and Redis 7 deployed in the kw cluster (novaforge-dev namespace), not mocks.
- `go vet ./...` exits 0. `go build ./...` exits 0.
- Merged in 6e60165. Implementing commits: 4faa176, 2bfa91b, ce4dd35, 39bb38e, b3e78ea.
