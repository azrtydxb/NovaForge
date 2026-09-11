# foundation 07: TOTP two-factor

Status: open
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

- [ ] Write the failing test `internal/identity/totp_test.go`: `func TestTOTPKnownVector(t *testing.T)` asserts `ValidateTOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "no-such-code", time.Unix(59,0))` is false, and that the code produced for `time.Unix(59,0)` validates at that instant; `func TestTOTPWindowTolerance(t *testing.T)` asserts a code generated at T validates at T+29s and fails at T+120s. Run `go test ./internal/identity/` — expect FAIL with "undefined: identity.ValidateTOTP".
- [ ] Implement RFC 6238 TOTP directly on `crypto/hmac` and `crypto/sha1` with a 30-second step and 6 digits, accepting a window of ±1 step. Encode secrets as base32 without padding.
- [ ] Implement `GenerateTOTPSecret` producing 20 random bytes and the provisioning URI `otpauth://totp/NovaForge:<username>?secret=<b32>&issuer=NovaForge`.
- [ ] Run `go test -run TestTOTP ./internal/identity/` — expect PASS.
- [ ] Commit as `feat: add RFC 6238 TOTP two-factor`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
