# intelligence 07: Graph query API

Status: closed 2026-09-11
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

- [x] Write the failing test `internal/graph/grpc_test.go`: `func TestDependentsAnswersForSymbol(t *testing.T)` seeds a graph where service `api` depends on symbol `UserService` and asserts `Dependents` for that symbol returns `api`; `func TestTestsCoveringAnswersForSymbol(t *testing.T)` asserts a `tested_by` edge surfaces the test file; `func TestLastChangedByReturnsWorkItem(t *testing.T)` asserts the most recent `changed_by` edge resolves to the Work Item key; `func TestGraphQueriesRejectForeignOrg(t *testing.T)` asserts `codes.PermissionDenied` for a scope belonging to another org. Run `go test ./internal/graph/` — expect FAIL with "undefined: graph.NewGRPCServer".
- [x] Implement every RPC deriving `org_id` from `authz.FromContext` and passing it as an explicit SQL predicate, never accepting it from the request message.
- [x] Run `TEST_DATABASE_URL=... go test ./internal/graph/` — expect PASS.
- [x] Commit as `feat: add graph query grpc service`.

## Evidence

- Task 7: the graph gRPC API, deriving org exclusively from authz.FromContext and distinguishing 'does not exist' (NotFound) from 'exists under another org' (PermissionDenied).
- Built by a parallel agent in an isolated git worktree under strict red-green TDD, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge by the main agent): ok indexing 5.254s, ok ctxasm 5.853s, ok graph 6.001s. 30 tests across the three packages, 0 failures.
- Run against the REAL PostgreSQL 16 with pgvector and Redis 7 in the kw cluster. Only the model Embedder and Reranker are in-process doubles.
- A real defect was found and fixed during the work rather than assumed away: the first run of TestBundleExcludesUnrelatedFiles leaked an unrelated file in through top-k semantic search, because top-k with no relevance cutoff always returns k results however poor. A cosine similarity floor was added and the test now pins it.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: 86c5e7a, 140c61d, d0c8204.
