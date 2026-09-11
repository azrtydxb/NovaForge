# foundation 04: Identity schema and user records

Status: open
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

- [ ] Write the failing test `internal/identity/store_test.go`: `func TestCreateUserAndLookup(t *testing.T)` creates a user and asserts `UserByUsername` returns the same ID; `func TestDuplicateUsernameRejected(t *testing.T)` asserts a second create with the same username returns an error containing "username". Run `go test ./internal/identity/` — expect FAIL with "undefined: identity.Store".
- [ ] Write the up migration creating `users` (id uuid pk, email citext unique not null, username citext unique not null, password_hash text not null, totp_secret text, created_at timestamptz not null default now()), `organizations` (id uuid pk, name citext unique not null, created_at timestamptz not null default now()), and `org_members` (org_id uuid not null references organizations(id) on delete cascade, user_id uuid not null references users(id) on delete cascade, role text not null check (role in ('owner','admin','member')), primary key (org_id, user_id)). Enable `citext` first with `CREATE EXTENSION IF NOT EXISTS citext;`.
- [ ] Write the matching down migration dropping the three tables in reverse dependency order.
- [ ] Implement `Store` over `*pgxpool.Pool`, mapping the unique-violation SQLSTATE `23505` to `fmt.Errorf("username %q already taken", username)`.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/identity/` — expect PASS.
- [ ] Commit as `feat: add identity schema with users, orgs, and memberships`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
