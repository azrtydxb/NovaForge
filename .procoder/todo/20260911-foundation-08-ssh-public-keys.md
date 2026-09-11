# foundation 08: SSH public keys

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 8 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/identity/migrations/000003_ssh_keys.up.sql`, `internal/identity/migrations/000003_ssh_keys.down.sql`, `internal/identity/sshkey.go`, `internal/identity/sshkey_test.go`

Interfaces: produces `identity.SSHKeyStore.Add(ctx, userID uuid.UUID, title, authorizedKey string) (SSHKey, error)` and `identity.SSHKeyStore.UserByFingerprint(ctx, fingerprint string) (uuid.UUID, error)`. The SSH server in Task 13 authenticates by calling `UserByFingerprint`.

## Acceptance criteria

- [x] Write the failing test `internal/identity/sshkey_test.go`: `func TestAddKeyComputesFingerprint(t *testing.T)` adds a known ed25519 authorized-key line and asserts the stored fingerprint starts with `SHA256:`; `func TestRejectMalformedKey(t *testing.T)` asserts `Add(ctx, user, "t", "not-a-key")` returns an error containing "parse". Run `go test ./internal/identity/` — expect FAIL with "undefined: identity.SSHKeyStore".
- [x] Write the up migration creating `ssh_keys` (id uuid pk, user_id uuid not null references users(id) on delete cascade, title text not null, fingerprint text not null unique, public_key text not null, created_at timestamptz not null default now()) and the matching down migration.
- [x] Implement `Add` parsing with `ssh.ParseAuthorizedKey` and computing the fingerprint with `ssh.FingerprintSHA256`, returning `fmt.Errorf("parse public key: %w", err)` on failure.
- [x] Run `TEST_DATABASE_URL=... go test ./internal/identity/` — expect PASS.
- [x] Commit as `feat: add ssh public key storage with sha256 fingerprints`.

## Evidence

- Task 8: ssh.ParseAuthorizedKey + ssh.FingerprintSHA256, malformed keys rejected with a parse error.
- Built by a parallel agent in an isolated git worktree, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent after the merge — an agent's report is a claim, not evidence.
- Red-green was followed per task by the implementing agent; the merged result was re-run from a clean checkout.
- Green (verified post-merge by the main agent): `go test ./internal/identity/ -count=1 -v` → 13 PASS, 0 FAIL, ok github.com/novaforge/novaforge/internal/identity 2.544s. Run against the REAL PostgreSQL 16 + pgvector and Redis 7 deployed in the kw cluster (novaforge-dev namespace), not mocks.
- `go vet ./...` exits 0. `go build ./...` exits 0.
- Merged in 6e60165. Implementing commits: 4faa176, 2bfa91b, ce4dd35, 39bb38e, b3e78ea.
