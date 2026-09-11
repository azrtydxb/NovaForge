# agent-runtime 06: go-ai-sdk integration and the agent run loop

Status: open
Created: 2026-09-11

## Description

Plan step 6 of `.procoder/plans/agent-runtime.md`, which exists to: Make AI agents first-class contributors: agent identities with provenance, Agent Runs executing in isolated per-run Kubernetes namespaces, a typed and fully audited tool surface instead of shell access, and all model access routed through go-ai-sdk so no provider-specific logic enters NovaForge.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/agentrun/loop.go`, `internal/agentrun/loop_test.go`, `internal/agentrun/model.go`

Interfaces: produces `agentrun.Loop.Execute(ctx context.Context, run agents.Run, reg *tools.Registry) (Result, error)` where `Result` is `{State string, Steps int, TokensUsed int64, Summary string}`, and `agentrun.NewModelClient(cfg ModelConfig) (aisdk.Model, error)` with `ModelConfig` carrying `Provider, Endpoint, Model string`. `NewModelClient` is the only place in NovaForge that names a provider, and it does so by passing the string through to go-ai-sdk.

## Acceptance criteria

- [ ] Write the failing test `internal/agentrun/loop_test.go`: `func TestLoopStopsOnBudget(t *testing.T)` drives the loop with a stub model that always requests another tool call and a 3-step budget, asserting the result state is `over_budget` and that provenance was still recorded; `func TestLoopRecordsEvidenceNotReasoning(t *testing.T)` drives a stub model returning both reasoning text and a tool call, then asserts the persisted rows contain the tool call and contain no field holding the reasoning text; `func TestLoopSurfacesToolErrors(t *testing.T)` asserts a tool returning an error is fed back to the model as a tool result rather than aborting the run. Run `go test ./internal/agentrun/` — expect FAIL with "undefined: agentrun.Loop".
- [ ] Add `go get github.com/azrtydxb/go-ai-sdk` and implement `NewModelClient` selecting the provider purely by the configured string, with no per-provider branch beyond passing `Endpoint` and `Model` through.
- [ ] Implement `Execute` as the loop: assemble the request, call the model through go-ai-sdk, dispatch any requested tool through `reg.Call`, append the result, and repeat until the model returns no tool call or the budget check fails.
- [ ] Persist only observable actions: the tool calls through the audit log and a final summary string. Never write model reasoning to any table.
- [ ] Run `go test ./internal/agentrun/` — expect PASS.
- [ ] Commit as `feat: add agent run loop over go-ai-sdk`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
