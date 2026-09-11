# factory 01: Work Item dependency edges

Status: open
Created: 2026-09-11

## Description

Plan step 1 of `.procoder/plans/factory.md`, which exists to: Close the loop from AI coding assistant to AI software engineering organization: decompose an epic into dependency-ordered subtasks across specialized agents, review changes with independent agents rather than their author, detect maintenance work autonomously and propose it as Work Items, and auto-merge only where policy already allows it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/work/migrations/000002_deps.up.sql`, `internal/work/migrations/000002_deps.down.sql`, `internal/work/deps.go`, `internal/work/deps_test.go`

Interfaces: produces `work.Store.AddDependency(ctx, blockedID, blockerID uuid.UUID) error`, `work.Store.Dependencies(ctx, id uuid.UUID) ([]Item, error)`, `work.Store.Dependents(ctx, id uuid.UUID) ([]Item, error)`, and `work.Store.Ready(ctx, orgID, epicID uuid.UUID) ([]Item, error)` returning only children whose every blocker is in state `done`.

## Acceptance criteria

- [ ] Write the up migration creating `work_item_deps` (blocked_id uuid not null references work_items(id) on delete cascade, blocker_id uuid not null references work_items(id) on delete cascade, primary key (blocked_id, blocker_id), check (blocked_id <> blocker_id)) and adding `parent_id uuid references work_items(id)` to `work_items`, plus the matching down migration.
- [ ] Write the failing test `internal/work/deps_test.go`: `func TestReadyExcludesBlockedItems(t *testing.T)` creates children `A` and `B` where `B` depends on `A` and asserts `Ready` returns only `A`; `func TestReadyIncludesUnblockedAfterDone(t *testing.T)` marks `A` done and asserts `Ready` then returns `B`; `func TestFailedBlockerKeepsDependentsBlocked(t *testing.T)` marks `A` as `blocked` and asserts `B` is still absent from `Ready`; `func TestCycleRejected(t *testing.T)` asserts adding a dependency that closes a cycle returns an error containing "cycle"; `func TestSelfDependencyRejected(t *testing.T)` asserts an item cannot depend on itself. Run `go test ./internal/work/` — expect FAIL with "undefined: work.Store.AddDependency".
- [ ] Implement `AddDependency` performing a recursive `WITH RECURSIVE` reachability check before inserting, returning `fmt.Errorf("dependency would create a cycle: %s -> %s", blocked, blocker)`.
- [ ] Implement `Ready` as a single query requiring `NOT EXISTS (SELECT 1 FROM work_item_deps d JOIN work_items b ON b.id = d.blocker_id WHERE d.blocked_id = w.id AND b.state <> 'done')`.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/work/` — expect PASS.
- [ ] Commit as `feat: add work item dependency edges with cycle rejection`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
