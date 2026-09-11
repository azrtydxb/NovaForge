# intelligence 04: Incremental indexer on push

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 4 of `.procoder/plans/intelligence.md`, which exists to: Give the platform an understanding of engineering relationships rather than files: a symbol and dependency index, an Engineering Graph joining code to Work Items, tests, ADRs and owners, bounded per-Work-Item context assembly with reranking, project-owned persistent knowledge, and NovaForge's own MCP server so external agents can drive all of it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/indexing/indexer.go`, `internal/indexing/indexer_test.go`

Interfaces: produces `indexing.Indexer.Run(ctx context.Context) error` consuming `events.StreamGitPush` in the consumer group `indexer`, and `indexing.Indexer.IndexCommit(ctx context.Context, orgID, repoID uuid.UUID, sha string, changedPaths []string) (indexed int, err error)`.

## Acceptance criteria

- [x] Write the failing test `internal/indexing/indexer_test.go`: `func TestIndexesOnlyChangedPaths(t *testing.T)` pushes a commit touching one file in a three-file repository and asserts `Parse` was invoked exactly once; `func TestDeletedFileRemovesSymbols(t *testing.T)` indexes a file then pushes its deletion and asserts its symbols and chunks are gone; `func TestRedeliveredPushIndexesOnce(t *testing.T)` delivers the same push event twice and asserts the resulting node set is identical to a single delivery; `func TestIndexerSurvivesParseError(t *testing.T)` asserts a file that fails to parse is skipped with a logged error while the remaining files still index. Run `go test ./internal/indexing/` — expect FAIL with "undefined: indexing.Indexer".
- [x] Implement `IndexCommit` fetching changed paths through the git service, parsing each, calling `ReplaceFileSubgraph` and `VectorStore.Upsert`, and recording the indexed SHA per repository so a redelivery of an already-indexed SHA returns immediately.
- [x] Implement the consumer loop with the same acknowledgement discipline as the CI scheduler: `XACK` only after the handler returns nil, plus an `XAUTOCLAIM` pass every 30s.
- [x] Run `TEST_DATABASE_URL=... TEST_REDIS_URL=... go test ./internal/indexing/` — expect PASS.
- [x] Commit as `feat: index symbols and chunks incrementally on push`.

## Evidence

- Task 4: the indexer consumes stream:git:push in group 'indexer', XACKs only after success with a 30s XAUTOCLAIM sweep, indexes only changed paths, treats a NotFound blob as a deletion and wipes its subgraph and chunks, and records the indexed SHA so redelivery is a no-op.
- Built by a parallel agent in an isolated git worktree under strict red-green TDD, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge by the main agent): ok indexing 5.254s, ok ctxasm 5.853s, ok graph 6.001s. 30 tests across the three packages, 0 failures.
- Run against the REAL PostgreSQL 16 with pgvector and Redis 7 in the kw cluster. Only the model Embedder and Reranker are in-process doubles.
- A real defect was found and fixed during the work rather than assumed away: the first run of TestBundleExcludesUnrelatedFiles leaked an unrelated file in through top-k semantic search, because top-k with no relevance cutoff always returns k results however poor. A cosine similarity floor was added and the test now pins it.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: 86c5e7a, 140c61d, d0c8204.
