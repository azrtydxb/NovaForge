# factory 05: Autonomous maintenance scanners

Status: open
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

- [ ] Write the failing test `internal/maintenance/scan_test.go`: `func TestScannersCoverAllEightKinds(t *testing.T)` asserts the keys of `maintenance.Scanners` are exactly the eight kinds above, sorted; `func TestCVEScannerProducesCriticalFinding(t *testing.T)` stubs the procoder security runner with a known advisory and asserts a finding of kind `cve` with severity `critical`; `func TestScannerFailureIsIsolated(t *testing.T)` asserts one scanner returning an error does not prevent the remaining seven from reporting. Run `go test ./internal/maintenance/` — expect FAIL with "undefined: maintenance.Scanners".
- [ ] Write the failing test `internal/maintenance/flaky_test.go`: `func TestFlakyDetectedFromMixedResults(t *testing.T)` seeds ten historical job results for one test name where the same commit SHA both passed and failed, and asserts a `flaky_test` finding naming it; `func TestConsistentFailureIsNotFlaky(t *testing.T)` asserts a test failing on every run produces no flaky finding, since that is a plain failure.
- [ ] Implement the dependency and CVE scanners by invoking the procoder deps and security commands, the coverage scanner by comparing the latest tests-gate coverage against the previous evaluation for the same repository, and the architectural scanner by reusing the architecture gate's import-graph check against the default branch.
- [ ] Implement the dead-code, documentation-drift, and performance-regression scanners against the graph service: symbols with no inbound `called_by` edge and no test coverage, context documents whose referenced symbols no longer exist, and benchmark artifacts whose latest value regressed more than the configured percentage.
- [ ] Run `go test ./internal/maintenance/` — expect PASS.
- [ ] Commit as `feat: add eight autonomous maintenance scanners`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
