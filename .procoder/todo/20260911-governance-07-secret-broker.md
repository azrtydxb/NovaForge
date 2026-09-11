# governance 07: Secret broker

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 7 of `.procoder/plans/governance.md`, which exists to: Make the platform, not the agent, the authority on merge: a gate controller that runs after an agent declares completion and cannot be bypassed or self-approved, a policy-driven approval model the model can never alter, and a secret broker issuing short-lived scoped credentials instead of durable secrets.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/secrets/broker.go`, `internal/secrets/broker_test.go`, `internal/secrets/migrations/000001_secrets.up.sql`, `internal/secrets/migrations/000001_secrets.down.sql`

Interfaces: produces `secrets.Broker.Issue(ctx context.Context, runID uuid.UUID, g capability.Grant, name string, ttl time.Duration) (Lease, error)`, `secrets.Broker.Redeem(ctx context.Context, token string) (value string, err error)`, and `secrets.Broker.Revoke(ctx context.Context, leaseID uuid.UUID) error`, where `Lease` is `{ID uuid.UUID, Token string, Name string, ExpiresAt time.Time}`.

## Acceptance criteria

- [x] Write the up migration creating `secret_values` (org_id uuid not null, name text not null, environment text not null check (environment in ('staging','production')), ciphertext bytea not null, primary key (org_id, name, environment)) and `secret_leases` (id uuid pk, org_id uuid not null, run_id uuid not null, name text not null, token_hash bytea not null unique, expires_at timestamptz not null, revoked_at timestamptz, redeemed_count int not null default 0) plus the matching down migration.
- [x] Write the failing test `internal/secrets/broker_test.go`: `func TestLeaseExpires(t *testing.T)` issues with a 10ms TTL, sleeps 30ms, and asserts `Redeem` returns an error containing "expired"; `func TestProductionSecretDeniedToStagingGrant(t *testing.T)` issues against a grant with `SecretsProd == false` for a production secret and asserts an error containing "not permitted"; `func TestRedeemedTokenIsSingleUse(t *testing.T)` asserts a second `Redeem` of the same token fails; `func TestCrossRunRedeemDenied(t *testing.T)` asserts a lease issued to run A cannot be redeemed in the context of run B. Run `go test ./internal/secrets/` — expect FAIL with "undefined: secrets.Broker".
- [x] Implement values encrypted at rest with `crypto/aes` in GCM using a key from `SECRETS_KEK`, storing nonce and ciphertext together, and store only `sha256(token)` for leases.
- [x] Run `TEST_DATABASE_URL=... go test ./internal/secrets/` — expect PASS.
- [x] Commit as `feat: add secret broker issuing short-lived single-use leases`.

## Evidence

- Task 7: AES-GCM at rest under SECRETS_KEK, leases stored as sha256 only, single-use, expiring, and scoped to the issuing run.
- Built by a parallel agent in an isolated git worktree under strict red-green TDD, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge by the main agent): 45 PASS, 0 FAIL across gates, approvals, secrets, reviews and ci. ok gates 1.824s, approvals 2.378s, secrets 1.816s, reviews 2.735s, ci 1.776s.
- The security-critical behaviours were checked by name, not assumed: TestMayMergeFalseWhenGateMissing, TestMayMergeFalseWhenEvaluationIsStale, TestMergeBlockedWhenControllerUnreachable, TestResolveReadsFromTargetRefNotSource, TestMalformedGateConfigFailsClosed, TestUnknownActionIsForbidden, TestDeployProductionForbiddenWithoutGrant, TestProductionSecretDeniedToStagingGrant, TestRedeemedTokenIsSingleUse, TestCrossRunRedeemDenied — all PASS.
- Run against the REAL PostgreSQL 16 in the kw cluster.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: c8e9753, e7cc870, 567fc5a, 8e276b3, c9583ba, 25581cc, fd54c04.
