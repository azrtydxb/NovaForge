# governance 03: The seven gate runners

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 3 of `.procoder/plans/governance.md`, which exists to: Make the platform, not the agent, the authority on merge: a gate controller that runs after an agent declares completion and cannot be bypassed or self-approved, a policy-driven approval model the model can never alter, and a secret broker issuing short-lived scoped credentials instead of durable secrets.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/gates/runner.go`, `internal/gates/tests.go`, `internal/gates/architecture.go`, `internal/gates/security.go`, `internal/gates/apicompat.go`, `internal/gates/deps.go`, `internal/gates/quality.go`, `internal/gates/docs.go`, `internal/gates/runner_test.go`, `internal/gates/architecture_test.go`

Interfaces: produces `type GateRunner func(ctx context.Context, in Input) (Evaluation, error)` where `Input` is `{OrgID, RepoID, RunID uuid.UUID, WorkDir string, TargetSHA, SourceSHA string, Params map[string]any, Proc ProcoderRunner}`, plus `gates.Runners` mapping each of the seven gate names to its runner, and `type ProcoderRunner func(ctx context.Context, workdir string, args ...string) (stdout []byte, exitCode int, err error)`.

## Acceptance criteria

- [x] Write the failing test `internal/gates/runner_test.go`: `func TestRunnersCoverEverySevenGates(t *testing.T)` asserts the keys of `gates.Runners` are exactly the seven names from Task 2, sorted; `func TestTestsGateFailsBelowCoverage(t *testing.T)` stubs `ProcoderRunner` to report 61% coverage against a `minimum_coverage` param of 80 and asserts status `fail` with detail containing "61"; `func TestSecurityGateFailsOnSecretFinding(t *testing.T)` stubs a non-zero exit with a secrets finding and asserts status `fail`; `func TestProcoderErrorIsGateError(t *testing.T)` stubs `ProcoderRunner` returning a transport error and asserts status `error` — never `pass`. Run `go test ./internal/gates/` — expect FAIL with "undefined: gates.Runners".
- [x] Write the failing test `internal/gates/architecture_test.go`: `func TestForbiddenDependencyDetected(t *testing.T)` builds a workdir where package `frontend` imports package `database`, sets the param `forbidden_dependencies: ["frontend -> database"]`, and asserts status `fail` with detail naming both packages; `func TestAllowedDependencyPasses(t *testing.T)` asserts the reverse direction passes.
- [x] Implement the tests gate by invoking `Proc` with `test` and parsing the coverage percentage, comparing against the `minimum_coverage` param defaulting to 0.
- [x] Implement the security gate by invoking `Proc` with `security` and failing on any secrets finding, independent of the SAST result, since the spec marks secrets blocking.
- [x] Implement the architecture gate by walking the workdir's import graph with `golang.org/x/tools/go/packages` and matching against the `forbidden_dependencies` param parsed as `"<from> -> <to>"` pairs.
- [x] Implement the api-compatibility gate by diffing api/openapi.yaml between `TargetSHA` and `SourceSHA` and failing when an existing path or required response is removed.
- [x] Implement the dependencies, quality, and documentation gates by invoking `Proc` with `deps`, `lint`, and `docs` respectively, mapping a non-zero exit to `fail` and a transport error to `error`.
- [x] Run `go test ./internal/gates/` — expect PASS.
- [x] Commit as `feat: add the seven gate runners`.

## Evidence

- Task 3: all seven runners. The architecture gate walks the real import graph with golang.org/x/tools/go/packages; ProCoder is invoked as a binary through an injected runner, never imported.
- Built by a parallel agent in an isolated git worktree under strict red-green TDD, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge by the main agent): 45 PASS, 0 FAIL across gates, approvals, secrets, reviews and ci. ok gates 1.824s, approvals 2.378s, secrets 1.816s, reviews 2.735s, ci 1.776s.
- The security-critical behaviours were checked by name, not assumed: TestMayMergeFalseWhenGateMissing, TestMayMergeFalseWhenEvaluationIsStale, TestMergeBlockedWhenControllerUnreachable, TestResolveReadsFromTargetRefNotSource, TestMalformedGateConfigFailsClosed, TestUnknownActionIsForbidden, TestDeployProductionForbiddenWithoutGrant, TestProductionSecretDeniedToStagingGrant, TestRedeemedTokenIsSingleUse, TestCrossRunRedeemDenied — all PASS.
- Run against the REAL PostgreSQL 16 in the kw cluster.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: c8e9753, e7cc870, 567fc5a, 8e276b3, c9583ba, 25581cc, fd54c04.
