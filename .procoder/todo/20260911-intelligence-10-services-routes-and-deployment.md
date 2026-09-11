# intelligence 10: Services, routes, and deployment

Status: open
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

- [ ] Extend `api/openapi.yaml` with `GET /api/v1/orgs/{org}/repos/{repo}/symbols/{name}`, `GET /api/v1/orgs/{org}/repos/{repo}/symbols/{name}/dependents`, `GET /api/v1/orgs/{org}/repos/{repo}/symbols/{name}/tests`, `GET /api/v1/orgs/{org}/repos/{repo}/search/code`, `GET|POST /api/v1/orgs/{org}/repos/{repo}/knowledge`, and `GET /api/v1/orgs/{org}/repos/{repo}/work/{key}/context`.
- [ ] Write the failing test `tests/e2e/intelligence_test.sh`: on the kind cluster, push a repository containing a function `UserService` called from a second file and a test file covering it, wait until `nf symbol UserService dependents` lists the caller within 120s, assert `nf symbol UserService tests` lists the test, record a knowledge entry, then assert `nf work context NF-1` returns a bundle that includes the entry and excludes an unrelated file. Run `bash tests/e2e/intelligence_test.sh` — expect FAIL with "Error: no matching deployment novaforge-engineering-graph".
- [ ] Implement `cmd/engineering-graph/main.go`: migrate schemas `graph` and `knowledge`, start the indexer consumer, serve gRPC on 9097, expose `/healthz`, and drain for 30s on SIGTERM.
- [ ] Write both Deployment templates, giving engineering-graph a memory request of `1Gi` since Tree-sitter parsing of large files is the heaviest allocation in the platform.
- [ ] Run `bash tests/e2e/intelligence_test.sh` — expect PASS.
- [ ] Commit as `feat: deploy engineering-graph and mcp-server`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
