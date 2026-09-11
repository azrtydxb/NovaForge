# agent-runtime 05: Typed agent tools

Status: open
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

- [ ] Write the failing test `internal/tools/registry_test.go`: `func TestRegistryHasExactlyThirteenTools(t *testing.T)` asserts `Names()` equals the thirteen names above, sorted — so adding an unlisted tool fails the build; `func TestUnknownToolRejected(t *testing.T)` asserts `Call` with name `"shell.exec"` returns an error containing "unknown tool"; `func TestEveryCallIsAudited(t *testing.T)` calls `work.get` and asserts exactly one audit entry exists for the run with that tool name; `func TestBudgetCheckedBeforeCall(t *testing.T)` exhausts the budget and asserts `Call` returns `agents.ErrOverBudget` without the handler ever running. Run `go test ./internal/tools/` — expect FAIL with "undefined: tools.Registry".
- [ ] Write the failing test `internal/tools/git_test.go`: `func TestGitCommitRefusesOutOfScopeBranch(t *testing.T)` builds a Runtime whose grant allows `agents/NF-1/` and asserts `git.commit` targeting `refs/heads/main` returns an error containing "not permitted", proving the tool path refuses identically to the transport path; `func TestWorkspaceWriteFileRejectsTraversal(t *testing.T)` asserts `workspace.write_file` with path `../../etc/passwd` returns an error containing "outside workspace".
- [ ] Implement `Call` so the order is fixed and testable: budget check, audit `Record`, capability check, handler, audit `Complete`. A handler is never reached when any earlier step fails.
- [ ] Implement each handler as a thin translation onto the existing gRPC services, with `git.commit` routing through `capability.CanWriteRef` and `workspace.write_file` resolving the path with `filepath.Clean` and rejecting any result escaping the workspace root.
- [ ] Run `go test ./internal/tools/` — expect PASS.
- [ ] Commit as `feat: add thirteen typed agent tools with audited dispatch`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
