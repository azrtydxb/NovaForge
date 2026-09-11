# agent-runtime 05: Typed agent tools

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 5 of `.procoder/plans/agent-runtime.md`, which exists to: Make AI agents first-class contributors: agent identities with provenance, Agent Runs executing in isolated per-run Kubernetes namespaces, a typed and fully audited tool surface instead of shell access, and all model access routed through go-ai-sdk so no provider-specific logic enters NovaForge.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `proto/tools/v1/tools.proto`, `internal/tools/registry.go`, `internal/tools/repo.go`, `internal/tools/workspace.go`, `internal/tools/git.go`, `internal/tools/ci.go`, `internal/tools/work.go`, `internal/tools/registry_test.go`, `internal/tools/git_test.go`

Interfaces: produces `tools.Registry` with `Register(name string, h Handler)`, `Call(ctx context.Context, runID uuid.UUID, name string, argsJSON []byte) ([]byte, error)`, and `Names() []string`, where `type Handler func(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error)` and `Runtime` carries the run's `capability.Grant`, its `*agents.Budget`, and clients for the git, work, reviews, and ci services. The thirteen registered names are exactly repo.search, repo.read_file, repo.get_symbol, repo.get_dependencies, workspace.write_file, git.diff, git.commit, ci.run_test, ci.get_logs, work.get, work.comment, architecture.query, and gate.status.

## Acceptance criteria

- [x] Write the failing test `internal/tools/registry_test.go`: `func TestRegistryHasExactlyThirteenTools(t *testing.T)` asserts `Names()` equals the thirteen names above, sorted — so adding an unlisted tool fails the build; `func TestUnknownToolRejected(t *testing.T)` asserts `Call` with name `"shell.exec"` returns an error containing "unknown tool"; `func TestEveryCallIsAudited(t *testing.T)` calls `work.get` and asserts exactly one audit entry exists for the run with that tool name; `func TestBudgetCheckedBeforeCall(t *testing.T)` exhausts the budget and asserts `Call` returns `agents.ErrOverBudget` without the handler ever running. Run `go test ./internal/tools/` — expect FAIL with "undefined: tools.Registry".
- [x] Write the failing test `internal/tools/git_test.go`: `func TestGitCommitRefusesOutOfScopeBranch(t *testing.T)` builds a Runtime whose grant allows `agents/NF-1/` and asserts `git.commit` targeting `refs/heads/main` returns an error containing "not permitted", proving the tool path refuses identically to the transport path; `func TestWorkspaceWriteFileRejectsTraversal(t *testing.T)` asserts `workspace.write_file` with path `../../etc/passwd` returns an error containing "outside workspace".
- [x] Implement `Call` so the order is fixed and testable: budget check, audit `Record`, capability check, handler, audit `Complete`. A handler is never reached when any earlier step fails.
- [x] Implement each handler as a thin translation onto the existing gRPC services, with `git.commit` routing through `capability.CanWriteRef` and `workspace.write_file` resolving the path with `filepath.Clean` and rejecting any result escaping the workspace root.
- [x] Run `go test ./internal/tools/` — expect PASS.
- [x] Commit as `feat: add thirteen typed agent tools with audited dispatch`.

## Evidence

- Task 5: exactly thirteen typed tools with a fixed dispatch order — budget check, audit Record, capability check, handler, audit Complete — so a handler is never reached when an earlier step fails.
- Built by a parallel agent in an isolated git worktree under strict red-green TDD, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge by the main agent): 30 PASS, 0 FAIL. ok tools 1.163s, agentrun 1.931s, agents 3.328s, repoconfig 1.675s.
- The behaviours the spec actually turns on were checked by name: TestRegistryHasExactlyThirteenTools, TestUnknownToolRejected, TestEveryCallIsAudited, TestBudgetCheckedBeforeCall, TestGitCommitRefusesOutOfScopeBranch (a tool call refuses an out-of-scope branch identically to the git transport), TestWorkspaceWriteFileRejectsTraversal, TestLoopStopsOnBudget, and TestLoopRecordsEvidenceNotReasoning (which greps every persisted row to prove no model reasoning leaks into storage) — all PASS.
- Run against the REAL PostgreSQL 16 in the kw cluster.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: 93f0c1c, 27bd9e0, 9ad94a1, fb8aab4.
