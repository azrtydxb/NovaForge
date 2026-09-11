# agent-runtime 08: Repository configuration under .novaforge

Status: open
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

- [ ] Write the failing test `internal/repoconfig/config_test.go`: `func TestLoadParsesProjectAndAgents(t *testing.T)` stubs the git client to return a project document and one agent document and asserts both parse; `func TestMalformedConfigFailsLoudly(t *testing.T)` returns invalid YAML and asserts the error contains "novaforge config" and that no partially populated `Config` is returned — a malformed config must never silently disable enforcement; `func TestAbsentConfigIsNotAnError(t *testing.T)` asserts a repository with no .novaforge directory yields the zero `Config` and a nil error; `func TestUnknownAgentToolRejected(t *testing.T)` asserts an agent listing a tool outside the thirteen registered names errors with "unknown tool". Run `go test ./internal/repoconfig/` — expect FAIL with "undefined: repoconfig.Load".
- [ ] Implement `Load` reading, through the git service, the paths .novaforge/project.yaml, then every file under .novaforge/agents/ and .novaforge/gates/, and listing .novaforge/context/ without reading it.
- [ ] Validate agent tool lists against `tools.Registry.Names()` so configuration cannot request a tool the platform does not implement.
- [ ] Run `go test ./internal/repoconfig/` — expect PASS.
- [ ] Commit as `feat: load repository agent and gate configuration from .novaforge`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
