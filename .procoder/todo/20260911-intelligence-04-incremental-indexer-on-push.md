# intelligence 04: Incremental indexer on push

Status: open
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

- [ ] Write the failing test `internal/indexing/indexer_test.go`: `func TestIndexesOnlyChangedPaths(t *testing.T)` pushes a commit touching one file in a three-file repository and asserts `Parse` was invoked exactly once; `func TestDeletedFileRemovesSymbols(t *testing.T)` indexes a file then pushes its deletion and asserts its symbols and chunks are gone; `func TestRedeliveredPushIndexesOnce(t *testing.T)` delivers the same push event twice and asserts the resulting node set is identical to a single delivery; `func TestIndexerSurvivesParseError(t *testing.T)` asserts a file that fails to parse is skipped with a logged error while the remaining files still index. Run `go test ./internal/indexing/` — expect FAIL with "undefined: indexing.Indexer".
- [ ] Implement `IndexCommit` fetching changed paths through the git service, parsing each, calling `ReplaceFileSubgraph` and `VectorStore.Upsert`, and recording the indexed SHA per repository so a redelivery of an already-indexed SHA returns immediately.
- [ ] Implement the consumer loop with the same acknowledgement discipline as the CI scheduler: `XACK` only after the handler returns nil, plus an `XAUTOCLAIM` pass every 30s.
- [ ] Run `TEST_DATABASE_URL=... TEST_REDIS_URL=... go test ./internal/indexing/` — expect PASS.
- [ ] Commit as `feat: index symbols and chunks incrementally on push`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
