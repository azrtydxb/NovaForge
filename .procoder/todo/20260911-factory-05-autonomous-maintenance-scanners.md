# factory 05: Autonomous maintenance scanners

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 5 of `.procoder/plans/factory.md`, which exists to: Close the loop from AI coding assistant to AI software engineering organization: decompose an epic into dependency-ordered subtasks across specialized agents, review changes with independent agents rather than their author, detect maintenance work autonomously and propose it as Work Items, and auto-merge only where policy already allows it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/maintenance/scan.go`, `internal/maintenance/deps.go`, `internal/maintenance/flaky.go`, `internal/maintenance/deadcode.go`, `internal/maintenance/coverage.go`, `internal/maintenance/docs.go`, `internal/maintenance/perf.go`, `internal/maintenance/arch.go`, `internal/maintenance/scan_test.go`, `internal/maintenance/flaky_test.go`

Interfaces: produces `maintenance.Finding{Kind, Title, Detail, Severity string, Paths []string, ProposedType string}` with `Kind` one of `outdated_dependency`, `cve`, `flaky_test`, `dead_code`, `coverage_regression`, `documentation_drift`, `performance_regression`, or `architectural_violation`; plus `type Scanner func(ctx context.Context, in ScanInput) ([]Finding, error)` and `maintenance.Scanners` mapping each kind to its scanner.

## Acceptance criteria

- [x] Write the failing test `internal/maintenance/scan_test.go`: `func TestScannersCoverAllEightKinds(t *testing.T)` asserts the keys of `maintenance.Scanners` are exactly the eight kinds above, sorted; `func TestCVEScannerProducesCriticalFinding(t *testing.T)` stubs the procoder security runner with a known advisory and asserts a finding of kind `cve` with severity `critical`; `func TestScannerFailureIsIsolated(t *testing.T)` asserts one scanner returning an error does not prevent the remaining seven from reporting. Run `go test ./internal/maintenance/` — expect FAIL with "undefined: maintenance.Scanners".
- [x] Write the failing test `internal/maintenance/flaky_test.go`: `func TestFlakyDetectedFromMixedResults(t *testing.T)` seeds ten historical job results for one test name where the same commit SHA both passed and failed, and asserts a `flaky_test` finding naming it; `func TestConsistentFailureIsNotFlaky(t *testing.T)` asserts a test failing on every run produces no flaky finding, since that is a plain failure.
- [x] Implement the dependency and CVE scanners by invoking the procoder deps and security commands, the coverage scanner by comparing the latest tests-gate coverage against the previous evaluation for the same repository, and the architectural scanner by reusing the architecture gate's import-graph check against the default branch.
- [x] Implement the dead-code, documentation-drift, and performance-regression scanners against the graph service: symbols with no inbound `called_by` edge and no test coverage, context documents whose referenced symbols no longer exist, and benchmark artifacts whose latest value regressed more than the configured percentage.
- [x] Run `go test ./internal/maintenance/` — expect PASS.
- [x] Commit as `feat: add eight autonomous maintenance scanners`.

## Evidence

- Task 5: all eight scanners as isolated pure functions, so one scanner's failure never blocks the rest.
- Built by a parallel agent in an isolated worktree under red-green TDD, merged to main, and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge): ok work 2.161s, swarm 4.292s, reviews 5.531s, maintenance 2.668s, 0 failures, against the REAL PostgreSQL 16 in the kw cluster.
- The invariants that matter were checked by name: TestAuthorAgentExcludedFromReviewers, TestAllApprovalsStillRequireGates, TestAutoMergeRefusedWhenGateFails, TestDecompositionCycleRejected, TestMaterialiseIsIdempotent, TestBlockedDependentNotStarted, TestFailedPrerequisiteBlocksDependents — all PASS.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: f7c0976, 22d95a8, 785f332, c180978, ef93f68, b12c13e, e868af3, 47dc030.
