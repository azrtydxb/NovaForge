# factory 07: Policy-controlled auto-merge

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 7 of `.procoder/plans/factory.md`, which exists to: Close the loop from AI coding assistant to AI software engineering organization: decompose an epic into dependency-ordered subtasks across specialized agents, review changes with independent agents rather than their author, detect maintenance work autonomously and propose it as Work Items, and auto-merge only where policy already allows it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/reviews/automerge.go`, `internal/reviews/automerge_test.go`

Interfaces: produces `reviews.AutoMergePolicy{Enabled bool, MaxFilesChanged int, AllowedTypes []string, ForbiddenPaths []string}` and `reviews.AutoMerger.Consider(ctx context.Context, runID uuid.UUID) (merged bool, reason string, err error)`. It calls the same `Merger.Merge` as a human would; it has no privileged path.

## Acceptance criteria

- [x] Write the failing test `internal/reviews/automerge_test.go`: `func TestAutoMergeRefusedWhenGateFails(t *testing.T)` asserts a failing gate yields `merged == false` with a reason naming the gate; `func TestAutoMergeRefusedForForbiddenPath(t *testing.T)` asserts a change touching `internal/auth/` under a policy forbidding it is refused even with all gates green; `func TestAutoMergeRefusedAboveFileCap(t *testing.T)` asserts a 40-file change under a cap of 20 is refused; `func TestAutoMergeRefusedWhenDisabled(t *testing.T)` asserts a disabled policy never merges; `func TestAutoMergeUsesTheSameMergePath(t *testing.T)` asserts `Consider` reaches `Merger.Merge` and therefore `MayMerge`, so auto-merge cannot bypass the controller. Run `go test ./internal/reviews/` — expect FAIL with "undefined: reviews.AutoMerger".
- [x] Implement `Consider` as a conjunction evaluated before delegating, with every refusal returning a human-readable reason recorded on the run.
- [x] Run `go test ./internal/reviews/` — expect PASS.
- [x] Commit as `feat: add policy-controlled auto-merge through the gate controller`.

## Evidence

- Task 7: auto-merge reaches Merger.Merge and therefore MayMerge, with no privileged path, proven with a spy GateChecker.
- Built by a parallel agent in an isolated worktree under red-green TDD, merged to main, and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge): ok work 2.161s, swarm 4.292s, reviews 5.531s, maintenance 2.668s, 0 failures, against the REAL PostgreSQL 16 in the kw cluster.
- The invariants that matter were checked by name: TestAuthorAgentExcludedFromReviewers, TestAllApprovalsStillRequireGates, TestAutoMergeRefusedWhenGateFails, TestDecompositionCycleRejected, TestMaterialiseIsIdempotent, TestBlockedDependentNotStarted, TestFailedPrerequisiteBlocksDependents — all PASS.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: f7c0976, 22d95a8, 785f332, c180978, ef93f68, b12c13e, e868af3, 47dc030.
