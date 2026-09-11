# work-ci 03: Change impact computation

Status: open
Created: 2026-09-11

## Description

Plan step 3 of `.procoder/plans/work-ci.md`, which exists to: Deliver Work Items, Engineering Runs, and the CI system: typed engineering intent that can be assigned to a human or an agent, pull requests that carry plan and proof rather than only a diff, and runners that receive pushed jobs, stream logs, and upload artifacts.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/reviews/impact.go`, `internal/reviews/impact_test.go`

Interfaces: produces `reviews.ComputeImpact(ctx context.Context, git gitv1.GitServiceClient, orgID, repoID uuid.UUID, from, to string) (Impact, error)` where `Impact` is `{FilesChanged int, Insertions int, Deletions int, Paths []string}`. The edge renders this as the CHANGE IMPACT block of an Engineering Run.

## Acceptance criteria

- [ ] Write the failing test `internal/reviews/impact_test.go`: `func TestComputeImpactCountsFiles(t *testing.T)` stubs `GitServiceClient.GetDiff` to return a two-file unified diff with three added and one removed line, then asserts `Impact{FilesChanged: 2, Insertions: 3, Deletions: 1}` and that `Paths` lists both files in order; `func TestComputeImpactEmptyDiff(t *testing.T)` asserts an empty diff yields a zero-valued `Impact` and a nil error. Run `go test ./internal/reviews/` — expect FAIL with "undefined: reviews.ComputeImpact".
- [ ] Implement `ComputeImpact` parsing the unified diff line-wise: count a file for each line beginning `diff --git `, extract the path from the `b/` side, count insertions for lines beginning `+` excluding `+++`, and deletions for lines beginning `-` excluding `---`.
- [ ] Run `go test ./internal/reviews/` — expect PASS.
- [ ] Commit as `feat: compute change impact from unified diff`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
