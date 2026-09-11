# foundation 05: Password hashing, login, and sessions

Status: open
Created: 2026-09-11

## Description

Plan step 5 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/identity/password.go`, `internal/identity/password_test.go`, `internal/identity/session.go`, `internal/identity/session_test.go`

Interfaces: produces `identity.HashPassword(plain string) (string, error)`, `identity.VerifyPassword(hash, plain string) bool`, `identity.SessionStore.Create(ctx, userID uuid.UUID, ttl time.Duration) (token string, err error)`, and `identity.SessionStore.Resolve(ctx, token string) (uuid.UUID, error)`. Sessions live in Redis, not PostgreSQL.

## Acceptance criteria

- [ ] Write the failing test `internal/identity/password_test.go`: `func TestHashVerifyRoundTrip(t *testing.T)` asserts `VerifyPassword(mustHash("correct horse"), "correct horse")` is true and `VerifyPassword(hash, "wrong")` is false; `func TestHashIsSalted(t *testing.T)` asserts two hashes of the same input differ. Run `go test ./internal/identity/` — expect FAIL with "undefined: identity.HashPassword".
- [ ] Add `go get golang.org/x/crypto` and implement hashing with `argon2.IDKey` using time=1, memory=64*1024, threads=4, keyLen=32 and a 16-byte crypto/rand salt, encoded as `$argon2id$v=19$m=65536,t=1,p=4$<b64salt>$<b64hash>`. `VerifyPassword` parses those parameters back out and compares with `subtle.ConstantTimeCompare`.
- [ ] Run `go test -run TestHash ./internal/identity/` — expect PASS.
- [ ] Write the failing test `internal/identity/session_test.go`: `func TestSessionResolve(t *testing.T)` creates a session and asserts `Resolve` returns the same user id; `func TestSessionExpired(t *testing.T)` creates with `ttl` of 1ms, sleeps 20ms, and asserts `Resolve` errors. Skip both when `TEST_REDIS_URL` is unset. Run `go test ./internal/identity/` — expect FAIL with "undefined: identity.SessionStore".
- [ ] Implement `SessionStore` over `*redis.Client`: `Create` generates 32 bytes from `crypto/rand`, encodes base64url as the token, and `SET session:<sha256(token)> <userID> EX <ttl>`. Store the hash, never the token itself.
- [ ] Run `TEST_REDIS_URL=redis://localhost:6379 go test ./internal/identity/` — expect PASS.
- [ ] Commit as `feat: add argon2id password hashing and redis-backed sessions`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
