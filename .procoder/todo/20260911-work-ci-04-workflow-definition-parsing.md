# work-ci 04: Workflow definition parsing

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 4 of `.procoder/plans/work-ci.md`, which exists to: Deliver Work Items, Engineering Runs, and the CI system: typed engineering intent that can be assigned to a human or an agent, pull requests that carry plan and proof rather than only a diff, and runners that receive pushed jobs, stream logs, and upload artifacts.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/ci/workflow.go`, `internal/ci/workflow_test.go`

Interfaces: produces `ci.Workflow{Name string, Jobs map[string]Job}` and `ci.Job{Run string, Agent string, Needs []string, Image string, Env map[string]string}`, plus `ci.ParseWorkflow(data []byte) (Workflow, error)` and `ci.TopoSort(w Workflow) ([]string, error)`. The scheduler in Task 5 consumes both. Workflow files live at `.novaforge/workflow.yaml` in the repository under test.

## Acceptance criteria

- [x] Write the failing test `internal/ci/workflow_test.go`: `func TestParseWorkflowWithAgentJob(t *testing.T)` parses the literal document `jobs:\n test:\n run: go test ./...\n security-review:\n agent: security\n` and asserts `Jobs["test"].Run == "go test ./..."` and `Jobs["security-review"].Agent == "security"`; `func TestRejectJobWithBothRunAndAgent(t *testing.T)` asserts a job declaring both returns an error containing "exactly one of"; `func TestTopoSortRespectsNeeds(t *testing.T)` asserts a workflow where `b` needs `a` sorts `a` before `b`; `func TestTopoSortDetectsCycle(t *testing.T)` asserts a workflow where `a` needs `b` and `b` needs `a` returns an error containing "cycle". Run `go test ./internal/ci/` — expect FAIL with "undefined: ci.ParseWorkflow".
- [x] Add `go get gopkg.in/yaml.v3` and implement `ParseWorkflow` with `yaml.Unmarshal`, rejecting a job with neither or both of `run` and `agent` using `fmt.Errorf("job %q must declare exactly one of run or agent", name)`, and rejecting a `needs` entry naming an unknown job.
- [x] Implement `TopoSort` as Kahn's algorithm over the `Needs` edges, returning `errors.New("workflow contains a dependency cycle")` when nodes remain unvisited, and sorting ready nodes by name so the order is deterministic across runs.
- [x] Run `go test ./internal/ci/` — expect PASS.
- [x] Commit as `feat: add ci workflow parsing with topological job ordering`.

## Evidence

- Task 4: ParseWorkflow rejects a job declaring neither or both of run and agent; TopoSort is Kahn's algorithm with sorted tie-breaking and explicit cycle detection.
- Built by a parallel agent in an isolated worktree under red-green TDD, merged to main, and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge, re-run just now on the merged tree): ok internal/work, ok internal/reviews, ok internal/ci, 0 failures, against the REAL PostgreSQL 16 in the kw cluster.
- `go build ./...` and `go vet ./...` exit 0.
- Implementing commits: 9444bd2, c1290c2, 8552d8e, 6de4be6.
