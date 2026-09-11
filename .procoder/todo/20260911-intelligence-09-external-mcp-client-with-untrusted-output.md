# intelligence 09: External MCP client with untrusted-output handling

Status: open
Created: 2026-09-11

## Description

Plan step 9 of `.procoder/plans/intelligence.md`, which exists to: Give the platform an understanding of engineering relationships rather than files: a symbol and dependency index, an Engineering Graph joining code to Work Items, tests, ADRs and owners, bounded per-Work-Item context assembly with reranking, project-owned persistent knowledge, and NovaForge's own MCP server so external agents can drive all of it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/mcp/client.go`, `internal/mcp/client_test.go`

Interfaces: produces `mcp.Client.Connect(ctx context.Context, def repoconfig.MCPServer) error`, `mcp.Client.ListTools(ctx context.Context) ([]ToolDef, error)`, and `mcp.Client.Call(ctx context.Context, name string, argsJSON []byte) (Result, error)` where `Result` carries `{Content string, Untrusted bool}` with `Untrusted` always true.

## Acceptance criteria

- [ ] Write the failing test `internal/mcp/client_test.go`: `func TestExternalResultIsMarkedUntrusted(t *testing.T)` asserts every `Result` has `Untrusted == true`, including on success; `func TestExternalCallRespectsTimeout(t *testing.T)` points the client at a server that never responds and asserts `Call` returns within the configured 30s deadline; `func TestUnreachableServerDoesNotFailTheRun(t *testing.T)` asserts a connect failure returns an error the agent loop can surface as a tool result rather than a panic; `func TestOnlyDeclaredServersAreDialled(t *testing.T)` asserts a server absent from .novaforge/mcp/ is refused with an error containing "not declared". Run `go test ./internal/mcp/` — expect FAIL with "undefined: mcp.Client".
- [ ] Implement `Call` wrapping content so the agent loop renders it inside an explicit untrusted-content boundary, and never interpreting any field of the response as a platform instruction.
- [ ] Run `go test ./internal/mcp/` — expect PASS.
- [ ] Commit as `feat: add external mcp client treating output as untrusted`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
