# foundation 07: TOTP two-factor

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 7 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/identity/totp.go`, `internal/identity/totp_test.go`

Interfaces: produces `identity.GenerateTOTPSecret() (secret string, uri string, err error)`, `identity.ValidateTOTP(secret, code string, at time.Time) bool`, and `identity.Store.SetTOTPSecret(ctx, userID uuid.UUID, secret string) error`. The login path in Task 12 calls `ValidateTOTP` whenever `User.TOTPSecret` is non-null.

## Acceptance criteria

- [x] Write the failing test `internal/identity/totp_test.go`: `func TestTOTPKnownVector(t *testing.T)` asserts `ValidateTOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "no-such-code", time.Unix(59,0))` is false, and that the code produced for `time.Unix(59,0)` validates at that instant; `func TestTOTPWindowTolerance(t *testing.T)` asserts a code generated at T validates at T+29s and fails at T+120s. Run `go test ./internal/identity/` — expect FAIL with "undefined: identity.ValidateTOTP".
- [x] Implement RFC 6238 TOTP directly on `crypto/hmac` and `crypto/sha1` with a 30-second step and 6 digits, accepting a window of ±1 step. Encode secrets as base32 without padding.
- [x] Implement `GenerateTOTPSecret` producing 20 random bytes and the provisioning URI `otpauth://totp/NovaForge:<username>?secret=<b32>&issuer=NovaForge`.
- [x] Run `go test -run TestTOTP ./internal/identity/` — expect PASS.
- [x] Commit as `feat: add RFC 6238 TOTP two-factor`.

## Evidence

- Task 7: RFC 6238 TOTP on crypto/hmac + crypto/sha1, 30s step, 6 digits, +/-1 step window.
- Built by a parallel agent in an isolated git worktree, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent after the merge — an agent's report is a claim, not evidence.
- Red-green was followed per task by the implementing agent; the merged result was re-run from a clean checkout.
- Green (verified post-merge by the main agent): `go test ./internal/identity/ -count=1 -v` → 13 PASS, 0 FAIL, ok github.com/novaforge/novaforge/internal/identity 2.544s. Run against the REAL PostgreSQL 16 + pgvector and Redis 7 deployed in the kw cluster (novaforge-dev namespace), not mocks.
- `go vet ./...` exits 0. `go build ./...` exits 0.
- Merged in 6e60165. Implementing commits: 4faa176, 2bfa91b, ce4dd35, 39bb38e, b3e78ea.
