# work-ci 01: Work Item schema and store

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 1 of `.procoder/plans/work-ci.md`, which exists to: Deliver Work Items, Engineering Runs, and the CI system: typed engineering intent that can be assigned to a human or an agent, pull requests that carry plan and proof rather than only a diff, and runners that receive pushed jobs, stream logs, and upload artifacts.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/work/migrations/000001_work.up.sql`, `internal/work/migrations/000001_work.down.sql`, `internal/work/store.go`, `internal/work/store_test.go`

Interfaces: produces `work.Item` with fields `ID uuid.UUID, OrgID uuid.UUID, RepoID uuid.UUID, Key string, Type string, Goal string, Acceptance []string, Constraints []string, RequiredGates []string, AssigneeID uuid.UUID, AssigneeKind string, State string, CreatedAt time.Time` and `work.Store` with `Create(ctx, Item) (Item, error)`, `Get(ctx, id uuid.UUID) (Item, error)`, `GetByKey(ctx, orgID uuid.UUID, key string) (Item, error)`, `List(ctx, orgID, repoID uuid.UUID, state string) ([]Item, error)`, and `Assign(ctx, id, assigneeID uuid.UUID, kind string) error`.

## Acceptance criteria

- [x] Write the failing test `internal/work/store_test.go`: `func TestCreateWorkItemAllocatesKey(t *testing.T)` creates two items in one org and asserts their keys are `NF-1` and `NF-2`; `func TestRejectUnknownType(t *testing.T)` asserts creating an item with type `"nonsense"` returns an error containing "invalid type"; `func TestAssignToAgent(t *testing.T)` assigns with kind `"agent"` and asserts `Get` round-trips it; `func TestListIsOrgScoped(t *testing.T)` creates items in two orgs and asserts a list for org A never returns org B's items. Run `go test ./internal/work/` — expect FAIL with "undefined: work.Store".
- [x] Write the up migration creating `work_items` (id uuid pk, org_id uuid not null, repo_id uuid not null, seq bigint not null, key text not null, type text not null check (type in ('feature','bug','refactor','security','tech_debt','research','architecture','upgrade','incident','documentation')), goal text not null, acceptance text[] not null default '{}', constraints text[] not null default '{}', required_gates text[] not null default '{}', assignee_id uuid, assignee_kind text check (assignee_kind in ('user','agent')), state text not null default 'open' check (state in ('open','planning','in_progress','review','done','blocked')), created_at timestamptz not null default now(), unique (org_id, key), unique (org_id, seq)) plus `CREATE INDEX ON work_items (org_id, repo_id, state);` and the matching down migration dropping the table.
- [x] Implement `Create` allocating `seq` inside the insert with `COALESCE((SELECT MAX(seq) FROM work_items WHERE org_id=$1), 0) + 1` in a single statement so concurrent creates cannot collide, and deriving `key` as `'NF-' || seq`.
- [x] Implement every read with an explicit `org_id = $1` predicate taken from `authz.FromContext`, never from an argument supplied by the caller's request body.
- [x] Run `TEST_DATABASE_URL=... go test ./internal/work/` — expect PASS.
- [x] Commit as `feat: add work item schema and store`.

## Evidence

- Task 1: work_items with a typed check constraint and the key NF-<seq> allocated inside the INSERT, so concurrent creates cannot collide.
- Built by a parallel agent in an isolated worktree under red-green TDD, merged to main, and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge, re-run just now on the merged tree): ok internal/work, ok internal/reviews, ok internal/ci, 0 failures, against the REAL PostgreSQL 16 in the kw cluster.
- `go build ./...` and `go vet ./...` exit 0.
- Implementing commits: 9444bd2, c1290c2, 8552d8e, 6de4be6.
