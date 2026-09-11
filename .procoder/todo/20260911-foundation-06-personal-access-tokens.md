# foundation 06: Personal access tokens

Status: open
Created: 2026-09-11

## Description

Plan step 6 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/identity/migrations/000002_tokens.up.sql`, `internal/identity/migrations/000002_tokens.down.sql`, `internal/identity/token.go`, `internal/identity/token_test.go`

Interfaces: produces `identity.TokenStore.Create(ctx, userID uuid.UUID, name string, scopes []string, expiresAt *time.Time) (plaintext string, t Token, err error)`, `identity.TokenStore.Resolve(ctx, plaintext string) (Token, error)`, and `identity.TokenStore.Revoke(ctx, id uuid.UUID) error`. `Token` carries `ID, UserID uuid.UUID, Name string, Scopes []string, ExpiresAt *time.Time, RevokedAt *time.Time`.

## Acceptance criteria

- [ ] Write the failing test `internal/identity/token_test.go`: `func TestTokenResolve(t *testing.T)` creates a token and asserts `Resolve(plaintext)` returns the matching user id; `func TestRevokedTokenRejected(t *testing.T)` revokes then asserts `Resolve` returns an error containing "revoked"; `func TestExpiredTokenRejected(t *testing.T)` creates with an `expiresAt` one hour in the past and asserts the error contains "expired". Run `go test ./internal/identity/` — expect FAIL with "undefined: identity.TokenStore".
- [ ] Write the up migration creating `access_tokens` (id uuid pk, user_id uuid not null references users(id) on delete cascade, name text not null, token_hash bytea not null unique, scopes text[] not null default '{}', expires_at timestamptz, revoked_at timestamptz, created_at timestamptz not null default now()) plus `CREATE INDEX ON access_tokens (token_hash);` and the matching down migration.
- [ ] Implement `Create` generating 32 random bytes rendered as `nf_<base64url>`, storing only `sha256(plaintext)` in `token_hash`, and returning the plaintext exactly once.
- [ ] Implement `Resolve` looking up by `sha256(plaintext)`, returning `errors.New("token revoked")` when `revoked_at` is set and `errors.New("token expired")` when `expires_at` is in the past.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/identity/` — expect PASS.
- [ ] Commit as `feat: add personal access tokens with hashed storage`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
