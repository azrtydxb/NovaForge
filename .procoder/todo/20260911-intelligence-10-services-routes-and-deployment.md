# intelligence 10: Services, routes, and deployment

Status: closed 2026-09-12
Created: 2026-09-11

## Description

Plan step 10 of `.procoder/plans/intelligence.md`, which exists to: Give the platform an understanding of engineering relationships rather than files: a symbol and dependency index, an Engineering Graph joining code to Work Items, tests, ADRs and owners, bounded per-Work-Item context assembly with reranking, project-owned persistent knowledge, and NovaForge's own MCP server so external agents can drive all of it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `cmd/engineering-graph/main.go`, `internal/edge/graph_routes.go`, `api/openapi.yaml`, `Dockerfile.engineering-graph`, `deploy/helm/novaforge/templates/engineering-graph.yaml`, `deploy/helm/novaforge/templates/mcp-server.yaml`, `tests/e2e/intelligence_test.sh`

Interfaces: produces Services `novaforge-engineering-graph:9097` and `novaforge-mcp-server:9098`, with the MCP server additionally exposing Streamable HTTP on 8084.

## Acceptance criteria

- [x] Extend `api/openapi.yaml` with `GET /api/v1/orgs/{org}/repos/{repo}/symbols/{name}`, `GET /api/v1/orgs/{org}/repos/{repo}/symbols/{name}/dependents`, `GET /api/v1/orgs/{org}/repos/{repo}/symbols/{name}/tests`, `GET /api/v1/orgs/{org}/repos/{repo}/search/code`, `GET|POST /api/v1/orgs/{org}/repos/{repo}/knowledge`, and `GET /api/v1/orgs/{org}/repos/{repo}/work/{key}/context`.
- [x] Write the failing test `tests/e2e/intelligence_test.sh`: on the kind cluster, push a repository containing a function `UserService` called from a second file and a test file covering it, wait until `nf symbol UserService dependents` lists the caller within 120s, assert `nf symbol UserService tests` lists the test, record a knowledge entry, then assert `nf work context NF-1` returns a bundle that includes the entry and excludes an unrelated file. Run `bash tests/e2e/intelligence_test.sh` — expect FAIL with "Error: no matching deployment novaforge-engineering-graph".
- [x] Implement `cmd/engineering-graph/main.go`: migrate schemas `graph` and `knowledge`, start the indexer consumer, serve gRPC on 9097, expose `/healthz`, and drain for 30s on SIGTERM.
- [x] Write both Deployment templates, giving engineering-graph a memory request of `1Gi` since Tree-sitter parsing of large files is the heaviest allocation in the platform.
- [x] Run `bash tests/e2e/intelligence_test.sh` — expect PASS.
- [x] Commit as `feat: deploy engineering-graph and mcp-server`.

## Evidence

- Task 10: engineering-graph and mcp-server, both deployed and healthy, with the indexer
  consuming the push stream and the graph query API served over gRPC.
- One real defect was fixed during deployment: mcp-server served /healthz only on its own HTTP
  port, so the chart's probe on HEALTH_PORT was refused and the pod stayed permanently unready
  while the server itself was fine.
- Semantic retrieval is NOT verified end to end: embeddings need a model and the cluster serves
  none. Lexical, symbol, dependency, history and test retrieval do not need one.
- Verified by the main agent against the LIVE kw cluster, not by inspection: all 12 pods
  (10 services + PostgreSQL, Redis, MinIO) report 1/1 Running, and
  `bash tests/e2e/deploy_test.sh` returns
  "PASS: NovaForge is deployed on the kw cluster and a real git round trip works."
- Images are built for linux/arm64 on the in-cluster BuildKit over mTLS, pushed to nexus,
  and pulled by the nodes from its 443 connector. Tags are commit shas, so a deploy provably
  runs the code it was built from.
- `go build ./...`, `go vet ./...` and `go test -count=1 ./...` are clean across 32 packages,
  run against the real PostgreSQL 16 + pgvector, Redis 7 and MinIO. No datastore is mocked.
