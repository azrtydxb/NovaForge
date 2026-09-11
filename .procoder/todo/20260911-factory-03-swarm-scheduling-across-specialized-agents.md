# factory 03: Swarm scheduling across specialized agents

Status: open
Created: 2026-09-11

## Description

Plan step 3 of `.procoder/plans/factory.md`, which exists to: Close the loop from AI coding assistant to AI software engineering organization: decompose an epic into dependency-ordered subtasks across specialized agents, review changes with independent agents rather than their author, detect maintenance work autonomously and propose it as Work Items, and auto-merge only where policy already allows it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/swarm/scheduler.go`, `internal/swarm/scheduler_test.go`

Interfaces: produces `swarm.Scheduler.Tick(ctx context.Context, epicID uuid.UUID) (started int, err error)` which starts an Agent Run for every ready subtask whose role maps to an enabled agent, and `swarm.Scheduler.Run(ctx context.Context) error` ticking every open epic on a 30s interval.

## Acceptance criteria

- [ ] Write the failing test `internal/swarm/scheduler_test.go`: `func TestTickStartsOnlyReadySubtasks(t *testing.T)` asserts a tick over the Enterprise SSO epic starts runs solely for the dependency-free subtasks; `func TestBlockedDependentNotStarted(t *testing.T)` asserts the OAuth backend subtask gets no run while the database subtask is unfinished; `func TestFailedPrerequisiteBlocksDependents(t *testing.T)` fails the database subtask and asserts the dependent never starts and the epic reports `blocked`; `func TestTickIsIdempotent(t *testing.T)` ticks twice with no state change in between and asserts the second tick starts zero runs; `func TestConcurrencyCapRespected(t *testing.T)` asserts no more than `MaxConcurrentRuns` runs are active for one epic. Run `go test ./internal/swarm/` — expect FAIL with "undefined: swarm.Scheduler".
- [ ] Implement `Tick` claiming each subtask with `UPDATE work_items SET state='in_progress' WHERE id=$1 AND state='open' RETURNING id`, so two concurrent ticks cannot start the same subtask twice.
- [ ] Implement the concurrency cap from the repository configuration key `project.max_concurrent_runs`, defaulting to 5.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/swarm/` — expect PASS.
- [ ] Commit as `feat: schedule swarm subtasks with dependency ordering`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
