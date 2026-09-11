# factory 07: Policy-controlled auto-merge

Status: open
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

- [ ] Write the failing test `internal/reviews/automerge_test.go`: `func TestAutoMergeRefusedWhenGateFails(t *testing.T)` asserts a failing gate yields `merged == false` with a reason naming the gate; `func TestAutoMergeRefusedForForbiddenPath(t *testing.T)` asserts a change touching `internal/auth/` under a policy forbidding it is refused even with all gates green; `func TestAutoMergeRefusedAboveFileCap(t *testing.T)` asserts a 40-file change under a cap of 20 is refused; `func TestAutoMergeRefusedWhenDisabled(t *testing.T)` asserts a disabled policy never merges; `func TestAutoMergeUsesTheSameMergePath(t *testing.T)` asserts `Consider` reaches `Merger.Merge` and therefore `MayMerge`, so auto-merge cannot bypass the controller. Run `go test ./internal/reviews/` — expect FAIL with "undefined: reviews.AutoMerger".
- [ ] Implement `Consider` as a conjunction evaluated before delegating, with every refusal returning a human-readable reason recorded on the run.
- [ ] Run `go test ./internal/reviews/` — expect PASS.
- [ ] Commit as `feat: add policy-controlled auto-merge through the gate controller`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
