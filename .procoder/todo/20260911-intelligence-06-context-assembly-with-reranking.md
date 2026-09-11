# intelligence 06: Context assembly with reranking

Status: open
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

- [ ] Write the failing test `internal/ctxasm/assemble_test.go`: `func TestBundleRespectsTokenBudget(t *testing.T)` supplies 200 candidate snippets and a budget of 1000 tokens and asserts `TokensEstimated <= 1000` and that the bundle is non-empty; `func TestBundleExcludesUnrelatedFiles(t *testing.T)` seeds a repository where only `internal/auth/` relates to the Work Item and asserts no snippet from `internal/billing/` appears — proving there is no whole-repository dump; `func TestAllSixSignalsContribute(t *testing.T)` asserts the candidate set before reranking contains at least one entry attributed to each of lexical, symbol, dependency, semantic, history, and test retrieval; `func TestStaleIndexDegradesNotFails(t *testing.T)` empties the symbol index and asserts `Assemble` still returns a bundle from the remaining signals with a nil error; `func TestKnowledgeIsIncluded(t *testing.T)` asserts a recorded decision related to the Work Item appears in `Bundle.Knowledge`. Run `go test ./internal/ctxasm/` — expect FAIL with "undefined: ctxasm.Assemble".
- [ ] Implement candidate gathering as six independent queries, each capped at 50 results and each tolerant of its own failure, tagging every candidate with the signal that produced it.
- [ ] Implement selection: rerank the deduplicated candidates against the Work Item goal, then take in score order while the running token estimate — computed as `len(text)/4` — stays under `TokenBudget`.
- [ ] Run `go test ./internal/ctxasm/` — expect PASS.
- [ ] Commit as `feat: assemble bounded work-item context from six signals`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
