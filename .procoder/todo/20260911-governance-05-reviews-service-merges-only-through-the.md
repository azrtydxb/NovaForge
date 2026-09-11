# governance 05: Reviews service merges only through the controller

Status: open
Created: 2026-09-11

## Description

Plan step 5 of `.procoder/plans/governance.md`, which exists to: Make the platform, not the agent, the authority on merge: a gate controller that runs after an agent declares completion and cannot be bypassed or self-approved, a policy-driven approval model the model can never alter, and a secret broker issuing short-lived scoped credentials instead of durable secrets.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/reviews/merge.go`, `internal/reviews/merge_test.go`

Interfaces: produces `reviews.Merger.Merge(ctx context.Context, runID uuid.UUID, method string) (mergeSHA string, err error)` where `method` is one of `merge`, `squash`, or `rebase`. It calls `gatesv1.GatesService.MayMerge` first and returns `reviews.ErrMergeBlocked` wrapping the reasons when not allowed.

## Acceptance criteria

- [ ] Write the failing test `internal/reviews/merge_test.go`: `func TestMergeBlockedByFailingGate(t *testing.T)` stubs `MayMerge` returning false and asserts `Merge` returns an error satisfying `errors.Is(err, reviews.ErrMergeBlocked)` and that the git service's merge RPC was never called; `func TestMergeBlockedWhenControllerUnreachable(t *testing.T)` stubs `MayMerge` returning a gRPC `codes.Unavailable` and asserts `Merge` returns an error and does not merge — proving the path fails closed rather than open; `func TestMergeProceedsWhenAllowed(t *testing.T)` asserts the merge SHA is returned and the run state becomes `merged`; `func TestMergeRequiresIndependentApproval(t *testing.T)` asserts a run with only the author's own approval is refused even when every gate passes. Run `go test ./internal/reviews/` — expect FAIL with "undefined: reviews.Merger".
- [ ] Implement `Merge` so the very first statement is the `MayMerge` call and every non-true result — including any transport error — returns before any git operation is attempted.
- [ ] Run `go test ./internal/reviews/` — expect PASS.
- [ ] Commit as `feat: route every merge through the gate controller`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
