# intelligence 03: Embeddings and semantic storage

Status: open
Created: 2026-09-11

## Description

Plan step 3 of `.procoder/plans/intelligence.md`, which exists to: Give the platform an understanding of engineering relationships rather than files: a symbol and dependency index, an Engineering Graph joining code to Work Items, tests, ADRs and owners, bounded per-Work-Item context assembly with reranking, project-owned persistent knowledge, and NovaForge's own MCP server so external agents can drive all of it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/graph/migrations/000002_vectors.up.sql`, `internal/graph/migrations/000002_vectors.down.sql`, `internal/graph/embed.go`, `internal/graph/embed_test.go`

Interfaces: produces `graph.Embedder.Embed(ctx context.Context, chunks []string) ([][]float32, error)` backed by go-ai-sdk, and `graph.VectorStore.Upsert(ctx, orgID, repoID uuid.UUID, path string, chunks []Chunk) error` plus `graph.VectorStore.Search(ctx, orgID, repoID uuid.UUID, query []float32, k int) ([]Chunk, error)` where `Chunk` is `{ID uuid.UUID, Path string, StartLine, EndLine int, Text string, Score float32}`.

## Acceptance criteria

- [ ] Write the up migration enabling pgvector with `CREATE EXTENSION IF NOT EXISTS vector;` and creating `code_chunks` (id uuid pk, org_id uuid not null, repo_id uuid not null, path text not null, start_line int not null, end_line int not null, text text not null, embedding vector(768) not null) plus `CREATE INDEX ON code_chunks USING hnsw (embedding vector_cosine_ops);` and the matching down migration.
- [ ] Write the failing test `internal/graph/embed_test.go`: `func TestSearchRanksNearestFirst(t *testing.T)` upserts three chunks with stub embeddings whose cosine distances are known and asserts `Search` returns them nearest-first; `func TestSearchIsOrgScoped(t *testing.T)` asserts a search under org A never returns org B's chunks even when their embeddings are identical; `func TestUpsertReplacesChunksForPath(t *testing.T)` upserts two chunks for a path then one and asserts only the latter remains; `func TestEmbedderFailureIsNotFatal(t *testing.T)` asserts an embedder error leaves the relational index intact and returns an error naming the path. Run `go test ./internal/graph/` — expect FAIL with "undefined: graph.VectorStore".
- [ ] Implement `Embedder` through go-ai-sdk's embedding interface, configured by `EMBED_ENDPOINT` and `EMBED_MODEL` so an air-gapped deployment points it at a local model and nothing in NovaForge names a provider.
- [ ] Chunk by symbol span from Task 1 where a symbol exists, and otherwise by 60-line windows with a 10-line overlap.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/graph/` — expect PASS.
- [ ] Commit as `feat: add pgvector chunk storage with go-ai-sdk embeddings`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
