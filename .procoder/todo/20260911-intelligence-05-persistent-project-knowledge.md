# intelligence 05: Persistent project knowledge

Status: open
Created: 2026-09-11

## Description

Plan step 5 of `.procoder/plans/intelligence.md`, which exists to: Give the platform an understanding of engineering relationships rather than files: a symbol and dependency index, an Engineering Graph joining code to Work Items, tests, ADRs and owners, bounded per-Work-Item context assembly with reranking, project-owned persistent knowledge, and NovaForge's own MCP server so external agents can drive all of it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/knowledge/migrations/000001_knowledge.up.sql`, `internal/knowledge/migrations/000001_knowledge.down.sql`, `internal/knowledge/store.go`, `internal/knowledge/store_test.go`

Interfaces: produces `knowledge.Entry{ID, OrgID, RepoID uuid.UUID, Key string, Kind string, Title, Body string, SourceRunID *uuid.UUID, CreatedAt time.Time}` with `Kind` one of `decision`, `pattern`, `incident`, `correction`, or `operational`; plus `knowledge.Store` with `Record`, `Get`, `Search(ctx, orgID, repoID uuid.UUID, query []float32, k int) ([]Entry, error)`, and `Supersede(ctx, oldID, newID uuid.UUID) error`.

## Acceptance criteria

- [ ] Write the up migration creating `knowledge_entries` (id uuid pk, org_id uuid not null, repo_id uuid not null, key text not null, kind text not null check (kind in ('decision','pattern','incident','correction','operational')), title text not null, body text not null, source_run_id uuid, superseded_by uuid references knowledge_entries(id), embedding vector(768), created_at timestamptz not null default now(), unique (org_id, repo_id, key)) plus an hnsw index on the embedding and the matching down migration.
- [ ] Write the failing test `internal/knowledge/store_test.go`: `func TestRecordAndSearch(t *testing.T)` records the entry titled `Never validate JWT tokens directly in route handlers` and asserts a semantically near query returns it; `func TestSupersededEntryExcludedFromSearch(t *testing.T)` supersedes an entry and asserts it no longer appears in results while remaining fetchable by id; `func TestKnowledgeIsRepoScoped(t *testing.T)` asserts a search for repo A never returns repo B's entries; `func TestCorrectionRecordsSourceRun(t *testing.T)` asserts an entry of kind `correction` retains the run it came from. Run `go test ./internal/knowledge/` — expect FAIL with "undefined: knowledge.Store".
- [ ] Implement `Search` filtering `superseded_by IS NULL` in the SQL itself rather than in Go, so a superseded decision can never reach an agent's context.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/knowledge/` — expect PASS.
- [ ] Commit as `feat: add project knowledge with supersession`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
