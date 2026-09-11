# intelligence 08: NovaForge MCP server

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 8 of `.procoder/plans/intelligence.md`, which exists to: Give the platform an understanding of engineering relationships rather than files: a symbol and dependency index, an Engineering Graph joining code to Work Items, tests, ADRs and owners, bounded per-Work-Item context assembly with reranking, project-owned persistent knowledge, and NovaForge's own MCP server so external agents can drive all of it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `cmd/mcp-server/main.go`, `internal/mcp/server.go`, `internal/mcp/tools.go`, `internal/mcp/server_test.go`, `Dockerfile.mcp-server`

Interfaces: produces `mcp.Server` with `ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error` and `ServeHTTP(w http.ResponseWriter, r *http.Request)` implementing Streamable HTTP. It exposes exactly seven tools: novaforge.get_work_item, novaforge.search_repository, novaforge.get_symbol, novaforge.create_branch, novaforge.get_review, novaforge.run_ci, and novaforge.get_gate_status.

## Acceptance criteria

- [x] Write the failing test `internal/mcp/server_test.go`: `func TestInitializeAdvertisesCurrentProtocolVersion(t *testing.T)` sends an `initialize` request over stdio and asserts the response advertises the current specification revision and does not offer the deprecated HTTP+SSE transport; `func TestToolsListReturnsExactlySeven(t *testing.T)` asserts `tools/list` returns the seven names above, sorted; `func TestCallToolRequiresAuth(t *testing.T)` asserts a `tools/call` without a resolvable token returns a JSON-RPC error whose message contains "unauthorized"; `func TestCallToolIsOrgScoped(t *testing.T)` asserts a token for org A calling `novaforge.get_work_item` on org B's item returns an error containing "denied"; `func TestStreamableHTTPRoundTrip(t *testing.T)` performs the same `tools/list` over Streamable HTTP and asserts an identical result. Run `go test ./internal/mcp/` — expect FAIL with "undefined: mcp.Server".
- [x] Implement JSON-RPC 2.0 framing directly over `encoding/json` with no legacy transport branch, so the deprecated HTTP+SSE path does not exist in the code at all.
- [x] Implement each tool as a thin call onto the existing gRPC services, resolving the caller's scope from a bearer token through the identity service before any dispatch.
- [x] Run `go test ./internal/mcp/` — expect PASS.
- [x] Commit as `feat: add novaforge mcp server on the current spec revision`.

## Evidence

- Task 8: the seven novaforge.* operations over stdio and Streamable HTTP, conforming to MCP revision 2025-06-18 only. There is deliberately NO code path for the deprecated HTTP+SSE transport, so it cannot be negotiated by accident, and a test asserts 'sse' never appears in the initialize result.
- Written by the main agent under red-green TDD: tests first, observed failing, then implemented.
- Green: `go test -count=1 -v ./internal/mcp/` → 10 PASS, 0 FAIL, ok 0.704s. Includes TestInitializeAdvertisesCurrentProtocolVersion, TestToolsListReturnsExactlySeven, TestCallToolRequiresAuth, TestCallToolIsOrgScoped, TestStreamableHTTPRoundTrip, TestUnknownMethodIsMethodNotFound, TestExternalResultIsMarkedUntrusted, TestOnlyDeclaredServersAreDialled, TestExternalCallRespectsTimeout, TestUnreachableServerDoesNotPanic.
- Writing the client surfaced a real defect rather than confirming an assumption: the declared-server allowlist was checked only in Connect, so a caller that skipped Connect could still reach an undeclared server. Call now enforces it independently and the test asserts both paths.
- `go vet ./internal/mcp/` exits 0.
