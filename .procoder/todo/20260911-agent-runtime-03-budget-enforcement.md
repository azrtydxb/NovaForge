# agent-runtime 03: Budget enforcement

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 3 of `.procoder/plans/agent-runtime.md`, which exists to: Make AI agents first-class contributors: agent identities with provenance, Agent Runs executing in isolated per-run Kubernetes namespaces, a typed and fully audited tool surface instead of shell access, and all model access routed through go-ai-sdk so no provider-specific logic enters NovaForge.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/agents/budget.go`, `internal/agents/budget_test.go`

Interfaces: produces `agents.Budget` with `NewBudget(wallclock time.Duration, tokens int64, costMicros int64) *Budget`, `(*Budget) AddTokens(n int64)`, `(*Budget) AddCostMicros(n int64)`, and `(*Budget) Check() error` returning `agents.ErrOverBudget` wrapped with the exceeded dimension. The run loop in Task 6 calls `Check` before every tool call and before every model call.

## Acceptance criteria

- [x] Write the failing test `internal/agents/budget_test.go`: `func TestWallclockExceeded(t *testing.T)` builds a budget with a 10ms wall-clock limit, sleeps 30ms, and asserts `Check` returns an error satisfying `errors.Is(err, agents.ErrOverBudget)` and containing "wallclock"; `func TestTokenLimitExceeded(t *testing.T)` adds 101 tokens against a limit of 100 and asserts the error contains "tokens"; `func TestCostLimitExceeded(t *testing.T)` asserts the same for cost; `func TestUnderBudgetPasses(t *testing.T)` asserts `Check` is nil when every dimension is below its limit. Run `go test ./internal/agents/` — expect FAIL with "undefined: agents.Budget".
- [x] Implement `Budget` with an atomic counter per dimension and a `started time.Time`, so concurrent tool calls accounting against the same budget are safe.
- [x] Run `go test ./internal/agents/` — expect PASS.
- [x] Commit as `feat: add agent run budget enforcement`.

## Evidence

- agent-runtime Task 3: Budget over atomic.Int64 for tokens and cost plus wallclock, safe under concurrent tool calls.
- Built by a parallel agent in an isolated git worktree under strict red-green TDD, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge by the main agent): 18 PASS, 0 FAIL. ok agents 1.613s, ok workspace 1.425s, ok gates 1.418s.
- TestBudgetConcurrentToolCallsAreSafe runs 100 goroutines and was additionally verified by the implementing agent under -race, clean.
- Postgres tests ran against the REAL PostgreSQL 16 in the kw cluster. Kubernetes tests use k8s.io/client-go/kubernetes/fake, which is the official clientset fake and the correct way to assert on created objects.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: ba4def9, d45a2d9, ffe166a, 3a49552, 23db4d3.
