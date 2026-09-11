# governance 04: The gate controller and merge authority

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 4 of `.procoder/plans/governance.md`, which exists to: Make the platform, not the agent, the authority on merge: a gate controller that runs after an agent declares completion and cannot be bypassed or self-approved, a policy-driven approval model the model can never alter, and a secret broker issuing short-lived scoped credentials instead of durable secrets.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/gates/controller.go`, `internal/gates/controller_test.go`

Interfaces: produces `gates.Controller.Evaluate(ctx context.Context, runID uuid.UUID) ([]Evaluation, error)` and `gates.Controller.MayMerge(ctx context.Context, runID uuid.UUID) (allowed bool, reasons []string, err error)`. The reviews service calls `MayMerge` and merges only on a literal `true`; it has no other path to merge.

## Acceptance criteria

- [x] Write the failing test `internal/gates/controller_test.go`: `func TestMayMergeFalseWhenGateFails(t *testing.T)` records `tests` as `fail` and asserts `allowed == false` with a reason naming `tests`; `func TestMayMergeFalseWhenGateMissing(t *testing.T)` records nothing and asserts `allowed == false` with a reason containing "not evaluated" — an unrun gate is never a pass; `func TestMayMergeFalseWhenEvaluationIsStale(t *testing.T)` records a pass for SHA `aaa` then advances the run's head to `bbb` and asserts `allowed == false`; `func TestMayMergeTrueWhenAllPass(t *testing.T)` asserts the only true case; `func TestGateConfigEditedInSourceIgnored(t *testing.T)` sets the source branch to declare zero gates while the target declares `tests`, records no evaluation, and asserts `allowed == false`. Run `go test ./internal/gates/` — expect FAIL with "undefined: gates.Controller".
- [x] Implement `MayMerge` as a deny-by-default fold: start from the resolved definitions, and for each required gate demand an evaluation for the run's current head SHA with status exactly `pass`. Anything else — missing, stale, `fail`, `error`, or `skipped` — appends a reason and leaves `allowed` false.
- [x] Implement `Evaluate` to run only gates whose latest evaluation does not already match the current head SHA, so re-evaluation is idempotent under redelivery.
- [x] Run `go test ./internal/gates/` — expect PASS.
- [x] Commit as `feat: add deny-by-default gate controller`.

## Evidence

- Task 4: deny-by-default MayMerge. Missing, stale, fail, error and skipped all mean not-allowed; only an evaluation with status exactly pass for the run's current head SHA counts.
- Built by a parallel agent in an isolated git worktree under strict red-green TDD, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge by the main agent): 45 PASS, 0 FAIL across gates, approvals, secrets, reviews and ci. ok gates 1.824s, approvals 2.378s, secrets 1.816s, reviews 2.735s, ci 1.776s.
- The security-critical behaviours were checked by name, not assumed: TestMayMergeFalseWhenGateMissing, TestMayMergeFalseWhenEvaluationIsStale, TestMergeBlockedWhenControllerUnreachable, TestResolveReadsFromTargetRefNotSource, TestMalformedGateConfigFailsClosed, TestUnknownActionIsForbidden, TestDeployProductionForbiddenWithoutGrant, TestProductionSecretDeniedToStagingGrant, TestRedeemedTokenIsSingleUse, TestCrossRunRedeemDenied — all PASS.
- Run against the REAL PostgreSQL 16 in the kw cluster.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: c8e9753, e7cc870, 567fc5a, 8e276b3, c9583ba, 25581cc, fd54c04.
