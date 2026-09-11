# governance 04: The gate controller and merge authority

Status: open
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

- [ ] Write the failing test `internal/gates/controller_test.go`: `func TestMayMergeFalseWhenGateFails(t *testing.T)` records `tests` as `fail` and asserts `allowed == false` with a reason naming `tests`; `func TestMayMergeFalseWhenGateMissing(t *testing.T)` records nothing and asserts `allowed == false` with a reason containing "not evaluated" — an unrun gate is never a pass; `func TestMayMergeFalseWhenEvaluationIsStale(t *testing.T)` records a pass for SHA `aaa` then advances the run's head to `bbb` and asserts `allowed == false`; `func TestMayMergeTrueWhenAllPass(t *testing.T)` asserts the only true case; `func TestGateConfigEditedInSourceIgnored(t *testing.T)` sets the source branch to declare zero gates while the target declares `tests`, records no evaluation, and asserts `allowed == false`. Run `go test ./internal/gates/` — expect FAIL with "undefined: gates.Controller".
- [ ] Implement `MayMerge` as a deny-by-default fold: start from the resolved definitions, and for each required gate demand an evaluation for the run's current head SHA with status exactly `pass`. Anything else — missing, stale, `fail`, `error`, or `skipped` — appends a reason and leaves `allowed` false.
- [ ] Implement `Evaluate` to run only gates whose latest evaluation does not already match the current head SHA, so re-evaluation is idempotent under redelivery.
- [ ] Run `go test ./internal/gates/` — expect PASS.
- [ ] Commit as `feat: add deny-by-default gate controller`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
