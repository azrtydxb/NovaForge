# intelligence 06: Context assembly with reranking

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 6 of `.procoder/plans/intelligence.md`, which exists to: Give the platform an understanding of engineering relationships rather than files: a symbol and dependency index, an Engineering Graph joining code to Work Items, tests, ADRs and owners, bounded per-Work-Item context assembly with reranking, project-owned persistent knowledge, and NovaForge's own MCP server so external agents can drive all of it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/ctxasm/assemble.go`, `internal/ctxasm/assemble_test.go`

Interfaces: produces `ctxasm.Assemble(ctx context.Context, in Input) (Bundle, error)` where `Input` is `{OrgID, RepoID uuid.UUID, WorkItem work.Item, TokenBudget int, Git gitv1.GitServiceClient, Graph *graph.Store, Vectors *graph.VectorStore, Knowledge *knowledge.Store, Reranker Reranker}` and `Bundle` is `{Files []Snippet, Knowledge []knowledge.Entry, Tests []string, TokensEstimated int}`. `type Reranker interface { Rank(ctx context.Context, query string, docs []string) ([]float32, error) }` is backed by go-ai-sdk.

## Acceptance criteria

- [x] Write the failing test `internal/ctxasm/assemble_test.go`: `func TestBundleRespectsTokenBudget(t *testing.T)` supplies 200 candidate snippets and a budget of 1000 tokens and asserts `TokensEstimated <= 1000` and that the bundle is non-empty; `func TestBundleExcludesUnrelatedFiles(t *testing.T)` seeds a repository where only `internal/auth/` relates to the Work Item and asserts no snippet from `internal/billing/` appears — proving there is no whole-repository dump; `func TestAllSixSignalsContribute(t *testing.T)` asserts the candidate set before reranking contains at least one entry attributed to each of lexical, symbol, dependency, semantic, history, and test retrieval; `func TestStaleIndexDegradesNotFails(t *testing.T)` empties the symbol index and asserts `Assemble` still returns a bundle from the remaining signals with a nil error; `func TestKnowledgeIsIncluded(t *testing.T)` asserts a recorded decision related to the Work Item appears in `Bundle.Knowledge`. Run `go test ./internal/ctxasm/` — expect FAIL with "undefined: ctxasm.Assemble".
- [x] Implement candidate gathering as six independent queries, each capped at 50 results and each tolerant of its own failure, tagging every candidate with the signal that produced it.
- [x] Implement selection: rerank the deduplicated candidates against the Work Item goal, then take in score order while the running token estimate — computed as `len(text)/4` — stays under `TokenBudget`.
- [x] Run `go test ./internal/ctxasm/` — expect PASS.
- [x] Commit as `feat: assemble bounded work-item context from six signals`.

## Evidence

- Task 6: Assemble gathers from six independently-failing signals, reranks, and fills a token budget in score order. Implemented in internal/ctxasm rather than the plan's internal/context, because a package named context shadows the standard library.
- Built by a parallel agent in an isolated git worktree under strict red-green TDD, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge by the main agent): ok indexing 5.254s, ok ctxasm 5.853s, ok graph 6.001s. 30 tests across the three packages, 0 failures.
- Run against the REAL PostgreSQL 16 with pgvector and Redis 7 in the kw cluster. Only the model Embedder and Reranker are in-process doubles.
- A real defect was found and fixed during the work rather than assumed away: the first run of TestBundleExcludesUnrelatedFiles leaked an unrelated file in through top-k semantic search, because top-k with no relevance cutoff always returns k results however poor. A cosine similarity floor was added and the test now pins it.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: 86c5e7a, 140c61d, d0c8204.
