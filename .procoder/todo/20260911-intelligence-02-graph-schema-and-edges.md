# intelligence 02: Graph schema and edges

Status: open
Created: 2026-09-11

## Description

Plan step 2 of `.procoder/plans/intelligence.md`, which exists to: Give the platform an understanding of engineering relationships rather than files: a symbol and dependency index, an Engineering Graph joining code to Work Items, tests, ADRs and owners, bounded per-Work-Item context assembly with reranking, project-owned persistent knowledge, and NovaForge's own MCP server so external agents can drive all of it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/graph/migrations/000001_graph.up.sql`, `internal/graph/migrations/000001_graph.down.sql`, `internal/graph/store.go`, `internal/graph/store_test.go`

Interfaces: produces `graph.Node{ID uuid.UUID, OrgID uuid.UUID, Kind, Key string, Attrs map[string]string}` with `Kind` one of `symbol`, `file`, `service`, `api`, `schema`, `test`, `work_item`, `commit`, `adr`, `deployment`, `owner`, or `incident`; `graph.Edge{FromID, ToID uuid.UUID, Kind string}` with `Kind` one of `depends_on`, `called_by`, `tested_by`, `owned_by`, `implements`, `deployed_as`, or `changed_by`; and `graph.Store` with `UpsertNode`, `UpsertEdge`, `Neighbours(ctx, nodeID uuid.UUID, edgeKind string, direction string) ([]Node, error)`, and `ReplaceFileSubgraph(ctx, orgID, repoID uuid.UUID, path string, nodes []Node, edges []Edge) error`.

## Acceptance criteria

- [ ] Write the up migration creating `graph_nodes` (id uuid pk, org_id uuid not null, repo_id uuid, kind text not null, key text not null, attrs jsonb not null default '{}', unique (org_id, kind, key)) and `graph_edges` (from_id uuid not null references graph_nodes(id) on delete cascade, to_id uuid not null references graph_nodes(id) on delete cascade, kind text not null, primary key (from_id, to_id, kind)) plus `CREATE INDEX ON graph_edges (to_id, kind);` and the matching down migration.
- [ ] Write the failing test `internal/graph/store_test.go`: `func TestNeighboursFollowsEdgeDirection(t *testing.T)` links service `api` `depends_on` symbol `UserService` and asserts an inbound query from `UserService` returns `api` while an outbound query returns nothing; `func TestUpsertNodeIsIdempotent(t *testing.T)` upserts the same key twice and asserts one row with the second call's attrs; `func TestReplaceFileSubgraphRemovesStaleSymbols(t *testing.T)` indexes a file with symbols `A` and `B`, re-indexes it with only `A`, and asserts `B` is gone — proving a deleted function does not linger; `func TestNeighboursIsOrgScoped(t *testing.T)` asserts a query under org A never traverses into org B. Run `go test ./internal/graph/` — expect FAIL with "undefined: graph.Store".
- [ ] Implement `ReplaceFileSubgraph` in one transaction: delete every node whose attrs carry the file path and whose kind is `symbol`, then insert the new set, so re-indexing is naturally idempotent under at-least-once redelivery.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/graph/` — expect PASS.
- [ ] Commit as `feat: add engineering graph schema with idempotent file subgraphs`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
