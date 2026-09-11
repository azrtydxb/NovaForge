# factory 02: Epic decomposition

Status: open
Created: 2026-09-11

## Description

Plan step 2 of `.procoder/plans/factory.md`, which exists to: Close the loop from AI coding assistant to AI software engineering organization: decompose an epic into dependency-ordered subtasks across specialized agents, review changes with independent agents rather than their author, detect maintenance work autonomously and propose it as Work Items, and auto-merge only where policy already allows it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/swarm/planner.go`, `internal/swarm/planner_test.go`

Interfaces: produces `swarm.Subtask{Title, Goal, Type, AgentRole string, DependsOn []string, Key string}` and `swarm.Planner.Decompose(ctx context.Context, epic work.Item, bundle ctxasm.Bundle) ([]Subtask, error)`, plus `swarm.Planner.Materialise(ctx context.Context, epic work.Item, subs []Subtask) ([]work.Item, error)` which writes the subtasks as Work Items with `parent_id` set and dependency edges created.

## Acceptance criteria

- [ ] Write the failing test `internal/swarm/planner_test.go`: `func TestDecomposeProducesOrderedSubtasks(t *testing.T)` drives a stub model returning the six subtasks of the Enterprise SSO example — database changes, OAuth backend, admin configuration, frontend, documentation, integration tests — and asserts the OAuth backend depends on the database changes; `func TestMaterialiseCreatesChildWorkItems(t *testing.T)` asserts six Work Items exist with `parent_id` equal to the epic and that `Ready` initially returns only the dependency-free ones; `func TestUnknownAgentRoleRejected(t *testing.T)` asserts a subtask naming a role absent from the repository's agent configuration returns an error containing "unknown agent role"; `func TestDecompositionCycleRejected(t *testing.T)` asserts a model returning mutually dependent subtasks is refused rather than materialised; `func TestMaterialiseIsIdempotent(t *testing.T)` calls `Materialise` twice with the same subtask keys and asserts six items exist, not twelve. Run `go test ./internal/swarm/` — expect FAIL with "undefined: swarm.Planner".
- [ ] Implement `Decompose` through go-ai-sdk structured output, requesting a strict schema of subtasks so the result is parsed rather than scraped from prose.
- [ ] Implement `Materialise` in one transaction, keyed on `(parent_id, key)` with `ON CONFLICT DO NOTHING`, and validating the whole dependency set against Task 1's cycle check before writing any row.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/swarm/` — expect PASS.
- [ ] Commit as `feat: decompose epics into dependency-ordered subtasks`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
