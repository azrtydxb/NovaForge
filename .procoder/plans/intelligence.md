# intelligence — implementation plan

Status: complete
Spec: .procoder/specs/backend-platform.md

## Goal

Give the platform an understanding of engineering relationships rather than files: a symbol and
dependency index, an Engineering Graph joining code to Work Items, tests, ADRs and owners,
bounded per-Work-Item context assembly with reranking, project-owned persistent knowledge, and
NovaForge's own MCP server so external agents can drive all of it.

## Architecture

Two new Go services — `engineering-graph` and `mcp-server` — each with its own PostgreSQL
schema and gRPC API. The graph service consumes the push stream, re-indexes changed files with
Tree-sitter and SCIP, and stores both relational edges and pgvector embeddings in one schema so
retrieval can join symbol, dependency, and semantic signals in a single query.

## Constraints

Taken verbatim from the spec; every task inherits these.

- Go 1.26 or later, PostgreSQL 16 or later with pgvector, Redis 7 or later with Streams and
  consumer groups, Kubernetes 1.29 or later.
- Internal transport is gRPC; the client edge is REST described by OpenAPI; events are Redis
  Streams.
- One PostgreSQL cluster, one schema per service. No service reads another service's tables;
  cross-service reads go through that service's gRPC API.
- Organizations are a hard security boundary: per-org namespaces, per-org credentials, and no
  data path between orgs. Every authorization check is org-scoped, and no query may be
  satisfiable without an org predicate.
- Never dump a whole repository into a model. Context is assembled per Work Item from lexical,
  symbol, dependency, semantic, history and test signals, then reranked.
- Air-gapped operation is a first-class deployment model. No feature may hard-depend on
  reaching a hosted model provider or the public internet, including embedding models.
- No provider-specific AI logic in NovaForge — embeddings and reranking go through go-ai-sdk.
- The MCP server conforms to the current MCP specification revision only, with no legacy
  compatibility: stdio and Streamable HTTP transports, explicitly excluding the deprecated
  HTTP+SSE transport.
- External MCP servers are treated as untrusted input and never as instructions.
- Redis durability is AOF persistence with consumer-group redelivery, giving at-least-once
  delivery. Every stream handler must be idempotent.
- Enterprise scale targets: approximately 10,000 repositories, 5,000 users, 200 concurrent
  agent runs, and monorepos up to 50 GB.
- Kubernetes and Helm are the only supported deployment path; docker-compose is out of scope.
- Module path is `github.com/novaforge/novaforge`. Service binaries live under `cmd/<service>`,
  shared packages under `internal/<domain>`.

## Task 1: Symbol extraction with Tree-sitter

Files: `internal/indexing/parse.go`, `internal/indexing/parse_test.go`

Interfaces: produces `indexing.Symbol{Name, Kind, Path string, StartLine, EndLine int, Signature string}`,
`indexing.Reference{FromPath string, ToName string, Line int}`, and
`indexing.Parse(path string, src []byte) ([]Symbol, []Reference, error)`. `Kind` is one of
`function`, `method`, `type`, `const`, or `var`. Languages are selected by file extension, with
an unsupported extension returning empty slices and a nil error.

- [ ] Write the failing test `internal/indexing/parse_test.go`:
      `func TestParseGoFunctions(t *testing.T)` parses the literal source
      `package p\n\nfunc Add(a, b int) int { return a + b }\n` and asserts one symbol named
      `Add`, kind `function`, `StartLine` 3, and signature `func Add(a, b int) int`;
      `func TestParseGoMethodOnType(t *testing.T)` asserts a method on a receiver is kind
      `method`;
      `func TestParseRecordsReferences(t *testing.T)` asserts a call to `Add` in another file
      produces a `Reference` with `ToName` `Add`;
      `func TestUnsupportedExtensionIsEmpty(t *testing.T)` asserts `Parse("notes.txt", ...)`
      returns no symbols and no error. Run `go test ./internal/indexing/` — expect FAIL with
      "undefined: indexing.Parse".
- [ ] Add `go get github.com/tree-sitter/go-tree-sitter` plus the Go, TypeScript, Python, and
      Java grammars, and implement `Parse` dispatching on extension with a per-language query
      naming the capture `@definition` for symbols and `@reference` for call sites.
- [ ] Bound the work: skip any file larger than 2 MiB and any file the grammar reports as having
      an error node covering more than half its span, so a 50 GB monorepo cannot stall indexing
      on generated blobs.
- [ ] Run `go test ./internal/indexing/` — expect PASS.
- [ ] Commit as `feat: extract symbols and references with tree-sitter`.

## Task 2: Graph schema and edges

Files: `internal/graph/migrations/000001_graph.up.sql`,
`internal/graph/migrations/000001_graph.down.sql`, `internal/graph/store.go`,
`internal/graph/store_test.go`

Interfaces: produces `graph.Node{ID uuid.UUID, OrgID uuid.UUID, Kind, Key string, Attrs map[string]string}`
with `Kind` one of `symbol`, `file`, `service`, `api`, `schema`, `test`, `work_item`, `commit`,
`adr`, `deployment`, `owner`, or `incident`; `graph.Edge{FromID, ToID uuid.UUID, Kind string}`
with `Kind` one of `depends_on`, `called_by`, `tested_by`, `owned_by`, `implements`,
`deployed_as`, or `changed_by`; and `graph.Store` with `UpsertNode`, `UpsertEdge`,
`Neighbours(ctx, nodeID uuid.UUID, edgeKind string, direction string) ([]Node, error)`, and
`ReplaceFileSubgraph(ctx, orgID, repoID uuid.UUID, path string, nodes []Node, edges []Edge) error`.

- [ ] Write the up migration creating `graph_nodes` (id uuid pk, org_id uuid not null, repo_id
      uuid, kind text not null, key text not null, attrs jsonb not null default '{}', unique
      (org_id, kind, key)) and `graph_edges` (from_id uuid not null references graph_nodes(id) on
      delete cascade, to_id uuid not null references graph_nodes(id) on delete cascade, kind text
      not null, primary key (from_id, to_id, kind)) plus
      `CREATE INDEX ON graph_edges (to_id, kind);` and the matching down migration.
- [ ] Write the failing test `internal/graph/store_test.go`:
      `func TestNeighboursFollowsEdgeDirection(t *testing.T)` links service `api` `depends_on`
      symbol `UserService` and asserts an inbound query from `UserService` returns `api` while an
      outbound query returns nothing;
      `func TestUpsertNodeIsIdempotent(t *testing.T)` upserts the same key twice and asserts one
      row with the second call's attrs;
      `func TestReplaceFileSubgraphRemovesStaleSymbols(t *testing.T)` indexes a file with symbols
      `A` and `B`, re-indexes it with only `A`, and asserts `B` is gone — proving a deleted
      function does not linger;
      `func TestNeighboursIsOrgScoped(t *testing.T)` asserts a query under org A never traverses
      into org B. Run `go test ./internal/graph/` — expect FAIL with "undefined: graph.Store".
- [ ] Implement `ReplaceFileSubgraph` in one transaction: delete every node whose attrs carry the
      file path and whose kind is `symbol`, then insert the new set, so re-indexing is naturally
      idempotent under at-least-once redelivery.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/graph/` — expect PASS.
- [ ] Commit as `feat: add engineering graph schema with idempotent file subgraphs`.

## Task 3: Embeddings and semantic storage

Files: `internal/graph/migrations/000002_vectors.up.sql`,
`internal/graph/migrations/000002_vectors.down.sql`, `internal/graph/embed.go`,
`internal/graph/embed_test.go`

Interfaces: produces
`graph.Embedder.Embed(ctx context.Context, chunks []string) ([][]float32, error)` backed by
go-ai-sdk, and `graph.VectorStore.Upsert(ctx, orgID, repoID uuid.UUID, path string, chunks []Chunk) error`
plus `graph.VectorStore.Search(ctx, orgID, repoID uuid.UUID, query []float32, k int) ([]Chunk, error)`
where `Chunk` is `{ID uuid.UUID, Path string, StartLine, EndLine int, Text string, Score float32}`.

- [ ] Write the up migration enabling pgvector with `CREATE EXTENSION IF NOT EXISTS vector;` and
      creating `code_chunks` (id uuid pk, org_id uuid not null, repo_id uuid not null, path text
      not null, start_line int not null, end_line int not null, text text not null, embedding
      vector(768) not null) plus
      `CREATE INDEX ON code_chunks USING hnsw (embedding vector_cosine_ops);` and the matching
      down migration.
- [ ] Write the failing test `internal/graph/embed_test.go`:
      `func TestSearchRanksNearestFirst(t *testing.T)` upserts three chunks with stub embeddings
      whose cosine distances are known and asserts `Search` returns them nearest-first;
      `func TestSearchIsOrgScoped(t *testing.T)` asserts a search under org A never returns org
      B's chunks even when their embeddings are identical;
      `func TestUpsertReplacesChunksForPath(t *testing.T)` upserts two chunks for a path then one
      and asserts only the latter remains;
      `func TestEmbedderFailureIsNotFatal(t *testing.T)` asserts an embedder error leaves the
      relational index intact and returns an error naming the path. Run `go test ./internal/graph/`
      — expect FAIL with "undefined: graph.VectorStore".
- [ ] Implement `Embedder` through go-ai-sdk's embedding interface, configured by
      `EMBED_ENDPOINT` and `EMBED_MODEL` so an air-gapped deployment points it at a local model
      and nothing in NovaForge names a provider.
- [ ] Chunk by symbol span from Task 1 where a symbol exists, and otherwise by 60-line windows
      with a 10-line overlap.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/graph/` — expect PASS.
- [ ] Commit as `feat: add pgvector chunk storage with go-ai-sdk embeddings`.

## Task 4: Incremental indexer on push

Files: `internal/indexing/indexer.go`, `internal/indexing/indexer_test.go`

Interfaces: produces `indexing.Indexer.Run(ctx context.Context) error` consuming
`events.StreamGitPush` in the consumer group `indexer`, and
`indexing.Indexer.IndexCommit(ctx context.Context, orgID, repoID uuid.UUID, sha string, changedPaths []string) (indexed int, err error)`.

- [ ] Write the failing test `internal/indexing/indexer_test.go`:
      `func TestIndexesOnlyChangedPaths(t *testing.T)` pushes a commit touching one file in a
      three-file repository and asserts `Parse` was invoked exactly once;
      `func TestDeletedFileRemovesSymbols(t *testing.T)` indexes a file then pushes its deletion
      and asserts its symbols and chunks are gone;
      `func TestRedeliveredPushIndexesOnce(t *testing.T)` delivers the same push event twice and
      asserts the resulting node set is identical to a single delivery;
      `func TestIndexerSurvivesParseError(t *testing.T)` asserts a file that fails to parse is
      skipped with a logged error while the remaining files still index. Run
      `go test ./internal/indexing/` — expect FAIL with "undefined: indexing.Indexer".
- [ ] Implement `IndexCommit` fetching changed paths through the git service, parsing each,
      calling `ReplaceFileSubgraph` and `VectorStore.Upsert`, and recording the indexed SHA per
      repository so a redelivery of an already-indexed SHA returns immediately.
- [ ] Implement the consumer loop with the same acknowledgement discipline as the CI scheduler:
      `XACK` only after the handler returns nil, plus an `XAUTOCLAIM` pass every 30s.
- [ ] Run `TEST_DATABASE_URL=... TEST_REDIS_URL=... go test ./internal/indexing/` — expect PASS.
- [ ] Commit as `feat: index symbols and chunks incrementally on push`.

## Task 5: Persistent project knowledge

Files: `internal/knowledge/migrations/000001_knowledge.up.sql`,
`internal/knowledge/migrations/000001_knowledge.down.sql`, `internal/knowledge/store.go`,
`internal/knowledge/store_test.go`

Interfaces: produces
`knowledge.Entry{ID, OrgID, RepoID uuid.UUID, Key string, Kind string, Title, Body string, SourceRunID *uuid.UUID, CreatedAt time.Time}`
with `Kind` one of `decision`, `pattern`, `incident`, `correction`, or `operational`; plus
`knowledge.Store` with `Record`, `Get`, `Search(ctx, orgID, repoID uuid.UUID, query []float32, k int) ([]Entry, error)`,
and `Supersede(ctx, oldID, newID uuid.UUID) error`.

- [ ] Write the up migration creating `knowledge_entries` (id uuid pk, org_id uuid not null,
      repo_id uuid not null, key text not null, kind text not null check (kind in
      ('decision','pattern','incident','correction','operational')), title text not null, body
      text not null, source_run_id uuid, superseded_by uuid references knowledge_entries(id),
      embedding vector(768), created_at timestamptz not null default now(), unique (org_id,
      repo_id, key)) plus an hnsw index on the embedding and the matching down migration.
- [ ] Write the failing test `internal/knowledge/store_test.go`:
      `func TestRecordAndSearch(t *testing.T)` records the entry titled
      `Never validate JWT tokens directly in route handlers` and asserts a semantically near
      query returns it;
      `func TestSupersededEntryExcludedFromSearch(t *testing.T)` supersedes an entry and asserts
      it no longer appears in results while remaining fetchable by id;
      `func TestKnowledgeIsRepoScoped(t *testing.T)` asserts a search for repo A never returns
      repo B's entries;
      `func TestCorrectionRecordsSourceRun(t *testing.T)` asserts an entry of kind `correction`
      retains the run it came from. Run `go test ./internal/knowledge/` — expect FAIL with
      "undefined: knowledge.Store".
- [ ] Implement `Search` filtering `superseded_by IS NULL` in the SQL itself rather than in Go,
      so a superseded decision can never reach an agent's context.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/knowledge/` — expect PASS.
- [ ] Commit as `feat: add project knowledge with supersession`.

## Task 6: Context assembly with reranking

Files: `internal/ctxasm/assemble.go`, `internal/ctxasm/assemble_test.go`

Interfaces: produces
`ctxasm.Assemble(ctx context.Context, in Input) (Bundle, error)` where `Input` is
`{OrgID, RepoID uuid.UUID, WorkItem work.Item, TokenBudget int, Git gitv1.GitServiceClient, Graph *graph.Store, Vectors *graph.VectorStore, Knowledge *knowledge.Store, Reranker Reranker}`
and `Bundle` is `{Files []Snippet, Knowledge []knowledge.Entry, Tests []string, TokensEstimated int}`.
`type Reranker interface { Rank(ctx context.Context, query string, docs []string) ([]float32, error) }`
is backed by go-ai-sdk.

- [ ] Write the failing test `internal/ctxasm/assemble_test.go`:
      `func TestBundleRespectsTokenBudget(t *testing.T)` supplies 200 candidate snippets and a
      budget of 1000 tokens and asserts `TokensEstimated <= 1000` and that the bundle is
      non-empty;
      `func TestBundleExcludesUnrelatedFiles(t *testing.T)` seeds a repository where only
      `internal/auth/` relates to the Work Item and asserts no snippet from `internal/billing/`
      appears — proving there is no whole-repository dump;
      `func TestAllSixSignalsContribute(t *testing.T)` asserts the candidate set before reranking
      contains at least one entry attributed to each of lexical, symbol, dependency, semantic,
      history, and test retrieval;
      `func TestStaleIndexDegradesNotFails(t *testing.T)` empties the symbol index and asserts
      `Assemble` still returns a bundle from the remaining signals with a nil error;
      `func TestKnowledgeIsIncluded(t *testing.T)` asserts a recorded decision related to the
      Work Item appears in `Bundle.Knowledge`. Run `go test ./internal/ctxasm/` — expect FAIL
      with "undefined: ctxasm.Assemble".
- [ ] Implement candidate gathering as six independent queries, each capped at 50 results and
      each tolerant of its own failure, tagging every candidate with the signal that produced it.
- [ ] Implement selection: rerank the deduplicated candidates against the Work Item goal, then
      take in score order while the running token estimate — computed as `len(text)/4` — stays
      under `TokenBudget`.
- [ ] Run `go test ./internal/ctxasm/` — expect PASS.
- [ ] Commit as `feat: assemble bounded work-item context from six signals`.

## Task 7: Graph query API

Files: `proto/graph/v1/graph.proto`, `internal/graph/grpc.go`, `internal/graph/grpc_test.go`

Interfaces: produces `novaforge.graph.v1.GraphService` with `GetSymbol`, `Dependents`,
`Dependencies`, `TestsCovering`, `LastChangedBy`, `SearchCode`, `AssembleContext`,
`RecordKnowledge`, and `SearchKnowledge`.

- [ ] Write the failing test `internal/graph/grpc_test.go`:
      `func TestDependentsAnswersForSymbol(t *testing.T)` seeds a graph where service `api`
      depends on symbol `UserService` and asserts `Dependents` for that symbol returns `api`;
      `func TestTestsCoveringAnswersForSymbol(t *testing.T)` asserts a `tested_by` edge surfaces
      the test file;
      `func TestLastChangedByReturnsWorkItem(t *testing.T)` asserts the most recent
      `changed_by` edge resolves to the Work Item key;
      `func TestGraphQueriesRejectForeignOrg(t *testing.T)` asserts `codes.PermissionDenied` for
      a scope belonging to another org. Run `go test ./internal/graph/` — expect FAIL with
      "undefined: graph.NewGRPCServer".
- [ ] Implement every RPC deriving `org_id` from `authz.FromContext` and passing it as an
      explicit SQL predicate, never accepting it from the request message.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/graph/` — expect PASS.
- [ ] Commit as `feat: add graph query grpc service`.

## Task 8: NovaForge MCP server

Files: `cmd/mcp-server/main.go`, `internal/mcp/server.go`, `internal/mcp/tools.go`,
`internal/mcp/server_test.go`, `Dockerfile.mcp-server`

Interfaces: produces `mcp.Server` with `ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error`
and `ServeHTTP(w http.ResponseWriter, r *http.Request)` implementing Streamable HTTP. It
exposes exactly seven tools: novaforge.get_work_item, novaforge.search_repository,
novaforge.get_symbol, novaforge.create_branch, novaforge.get_review, novaforge.run_ci, and
novaforge.get_gate_status.

- [ ] Write the failing test `internal/mcp/server_test.go`:
      `func TestInitializeAdvertisesCurrentProtocolVersion(t *testing.T)` sends an `initialize`
      request over stdio and asserts the response advertises the current specification revision
      and does not offer the deprecated HTTP+SSE transport;
      `func TestToolsListReturnsExactlySeven(t *testing.T)` asserts `tools/list` returns the seven
      names above, sorted;
      `func TestCallToolRequiresAuth(t *testing.T)` asserts a `tools/call` without a resolvable
      token returns a JSON-RPC error whose message contains "unauthorized";
      `func TestCallToolIsOrgScoped(t *testing.T)` asserts a token for org A calling
      `novaforge.get_work_item` on org B's item returns an error containing "denied";
      `func TestStreamableHTTPRoundTrip(t *testing.T)` performs the same `tools/list` over
      Streamable HTTP and asserts an identical result. Run `go test ./internal/mcp/` — expect
      FAIL with "undefined: mcp.Server".
- [ ] Implement JSON-RPC 2.0 framing directly over `encoding/json` with no legacy transport
      branch, so the deprecated HTTP+SSE path does not exist in the code at all.
- [ ] Implement each tool as a thin call onto the existing gRPC services, resolving the caller's
      scope from a bearer token through the identity service before any dispatch.
- [ ] Run `go test ./internal/mcp/` — expect PASS.
- [ ] Commit as `feat: add novaforge mcp server on the current spec revision`.

## Task 9: External MCP client with untrusted-output handling

Files: `internal/mcp/client.go`, `internal/mcp/client_test.go`

Interfaces: produces
`mcp.Client.Connect(ctx context.Context, def repoconfig.MCPServer) error`,
`mcp.Client.ListTools(ctx context.Context) ([]ToolDef, error)`, and
`mcp.Client.Call(ctx context.Context, name string, argsJSON []byte) (Result, error)` where
`Result` carries `{Content string, Untrusted bool}` with `Untrusted` always true.

- [ ] Write the failing test `internal/mcp/client_test.go`:
      `func TestExternalResultIsMarkedUntrusted(t *testing.T)` asserts every `Result` has
      `Untrusted == true`, including on success;
      `func TestExternalCallRespectsTimeout(t *testing.T)` points the client at a server that
      never responds and asserts `Call` returns within the configured 30s deadline;
      `func TestUnreachableServerDoesNotFailTheRun(t *testing.T)` asserts a connect failure
      returns an error the agent loop can surface as a tool result rather than a panic;
      `func TestOnlyDeclaredServersAreDialled(t *testing.T)` asserts a server absent from
      .novaforge/mcp/ is refused with an error containing "not declared". Run
      `go test ./internal/mcp/` — expect FAIL with "undefined: mcp.Client".
- [ ] Implement `Call` wrapping content so the agent loop renders it inside an explicit
      untrusted-content boundary, and never interpreting any field of the response as a platform
      instruction.
- [ ] Run `go test ./internal/mcp/` — expect PASS.
- [ ] Commit as `feat: add external mcp client treating output as untrusted`.

## Task 10: Services, routes, and deployment

Files: `cmd/engineering-graph/main.go`, `internal/edge/graph_routes.go`, `api/openapi.yaml`,
`Dockerfile.engineering-graph`, `deploy/helm/novaforge/templates/engineering-graph.yaml`,
`deploy/helm/novaforge/templates/mcp-server.yaml`, `tests/e2e/intelligence_test.sh`

Interfaces: produces Services `novaforge-engineering-graph:9097` and `novaforge-mcp-server:9098`,
with the MCP server additionally exposing Streamable HTTP on 8084.

- [ ] Extend `api/openapi.yaml` with `GET /api/v1/orgs/{org}/repos/{repo}/symbols/{name}`,
      `GET /api/v1/orgs/{org}/repos/{repo}/symbols/{name}/dependents`,
      `GET /api/v1/orgs/{org}/repos/{repo}/symbols/{name}/tests`,
      `GET /api/v1/orgs/{org}/repos/{repo}/search/code`,
      `GET|POST /api/v1/orgs/{org}/repos/{repo}/knowledge`, and
      `GET /api/v1/orgs/{org}/repos/{repo}/work/{key}/context`.
- [ ] Write the failing test `tests/e2e/intelligence_test.sh`: on the kind cluster, push a
      repository containing a function `UserService` called from a second file and a test file
      covering it, wait until `nf symbol UserService dependents` lists the caller within 120s,
      assert `nf symbol UserService tests` lists the test, record a knowledge entry, then assert
      `nf work context NF-1` returns a bundle that includes the entry and excludes an unrelated
      file. Run `bash tests/e2e/intelligence_test.sh` — expect FAIL with "Error: no matching
      deployment novaforge-engineering-graph".
- [ ] Implement `cmd/engineering-graph/main.go`: migrate schemas `graph` and `knowledge`, start
      the indexer consumer, serve gRPC on 9097, expose `/healthz`, and drain for 30s on SIGTERM.
- [ ] Write both Deployment templates, giving engineering-graph a memory request of `1Gi` since
      Tree-sitter parsing of large files is the heaviest allocation in the platform.
- [ ] Run `bash tests/e2e/intelligence_test.sh` — expect PASS.
- [ ] Commit as `feat: deploy engineering-graph and mcp-server`.
