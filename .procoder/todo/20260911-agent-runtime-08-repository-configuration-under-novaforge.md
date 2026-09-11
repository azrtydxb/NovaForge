# agent-runtime 08: Repository configuration under .novaforge

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 8 of `.procoder/plans/agent-runtime.md`, which exists to: Make AI agents first-class contributors: agent identities with provenance, Agent Runs executing in isolated per-run Kubernetes namespaces, a typed and fully audited tool surface instead of shell access, and all model access routed through go-ai-sdk so no provider-specific logic enters NovaForge.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/repoconfig/config.go`, `internal/repoconfig/config_test.go`

Interfaces: produces `repoconfig.Project{Name string, DefaultAgent string, Gates []string}`, `repoconfig.AgentDef{Name, Role, Model string, Tools []string}`, `repoconfig.GateDef{Name string, Params map[string]any}`, and `repoconfig.Load(ctx context.Context, git gitv1.GitServiceClient, orgID, repoID uuid.UUID, ref string) (Config, error)` where `Config` holds the project, the agents, the gates, and the context document paths.

## Acceptance criteria

- [x] Write the failing test `internal/repoconfig/config_test.go`: `func TestLoadParsesProjectAndAgents(t *testing.T)` stubs the git client to return a project document and one agent document and asserts both parse; `func TestMalformedConfigFailsLoudly(t *testing.T)` returns invalid YAML and asserts the error contains "novaforge config" and that no partially populated `Config` is returned — a malformed config must never silently disable enforcement; `func TestAbsentConfigIsNotAnError(t *testing.T)` asserts a repository with no .novaforge directory yields the zero `Config` and a nil error; `func TestUnknownAgentToolRejected(t *testing.T)` asserts an agent listing a tool outside the thirteen registered names errors with "unknown tool". Run `go test ./internal/repoconfig/` — expect FAIL with "undefined: repoconfig.Load".
- [x] Implement `Load` reading, through the git service, the paths .novaforge/project.yaml, then every file under .novaforge/agents/ and .novaforge/gates/, and listing .novaforge/context/ without reading it.
- [x] Validate agent tool lists against `tools.Registry.Names()` so configuration cannot request a tool the platform does not implement.
- [x] Run `go test ./internal/repoconfig/` — expect PASS.
- [x] Commit as `feat: load repository agent and gate configuration from .novaforge`.

## Evidence

- Task 8: .novaforge config loading that fails loudly on malformed YAML with no partially populated Config, and treats an absent directory as not an error.
- Built by a parallel agent in an isolated git worktree under strict red-green TDD, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge by the main agent): 30 PASS, 0 FAIL. ok tools 1.163s, agentrun 1.931s, agents 3.328s, repoconfig 1.675s.
- The behaviours the spec actually turns on were checked by name: TestRegistryHasExactlyThirteenTools, TestUnknownToolRejected, TestEveryCallIsAudited, TestBudgetCheckedBeforeCall, TestGitCommitRefusesOutOfScopeBranch (a tool call refuses an out-of-scope branch identically to the git transport), TestWorkspaceWriteFileRejectsTraversal, TestLoopStopsOnBudget, and TestLoopRecordsEvidenceNotReasoning (which greps every persisted row to prove no model reasoning leaks into storage) — all PASS.
- Run against the REAL PostgreSQL 16 in the kw cluster.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: 93f0c1c, 27bd9e0, 9ad94a1, fb8aab4.
