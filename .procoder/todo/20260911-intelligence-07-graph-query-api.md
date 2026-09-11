# intelligence 07: Graph query API

Status: open
Created: 2026-09-11

## Description

Plan step 7 of `.procoder/plans/intelligence.md`, which exists to: Give the platform an understanding of engineering relationships rather than files: a symbol and dependency index, an Engineering Graph joining code to Work Items, tests, ADRs and owners, bounded per-Work-Item context assembly with reranking, project-owned persistent knowledge, and NovaForge's own MCP server so external agents can drive all of it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `proto/graph/v1/graph.proto`, `internal/graph/grpc.go`, `internal/graph/grpc_test.go`

Interfaces: produces `novaforge.graph.v1.GraphService` with `GetSymbol`, `Dependents`, `Dependencies`, `TestsCovering`, `LastChangedBy`, `SearchCode`, `AssembleContext`, `RecordKnowledge`, and `SearchKnowledge`.

## Acceptance criteria

- [ ] Write the failing test `internal/graph/grpc_test.go`: `func TestDependentsAnswersForSymbol(t *testing.T)` seeds a graph where service `api` depends on symbol `UserService` and asserts `Dependents` for that symbol returns `api`; `func TestTestsCoveringAnswersForSymbol(t *testing.T)` asserts a `tested_by` edge surfaces the test file; `func TestLastChangedByReturnsWorkItem(t *testing.T)` asserts the most recent `changed_by` edge resolves to the Work Item key; `func TestGraphQueriesRejectForeignOrg(t *testing.T)` asserts `codes.PermissionDenied` for a scope belonging to another org. Run `go test ./internal/graph/` — expect FAIL with "undefined: graph.NewGRPCServer".
- [ ] Implement every RPC deriving `org_id` from `authz.FromContext` and passing it as an explicit SQL predicate, never accepting it from the request message.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/graph/` — expect PASS.
- [ ] Commit as `feat: add graph query grpc service`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
