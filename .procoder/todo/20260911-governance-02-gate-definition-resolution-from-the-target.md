# governance 02: Gate definition resolution from the target ref

Status: open
Created: 2026-09-11

## Description

Plan step 2 of `.procoder/plans/governance.md`, which exists to: Make the platform, not the agent, the authority on merge: a gate controller that runs after an agent declares completion and cannot be bypassed or self-approved, a policy-driven approval model the model can never alter, and a secret broker issuing short-lived scoped credentials instead of durable secrets.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/gates/resolve.go`, `internal/gates/resolve_test.go`

Interfaces: produces `gates.Resolve(ctx context.Context, git gitv1.GitServiceClient, orgID, repoID uuid.UUID, targetRef string, workItemGates []string) ([]Definition, error)`. It reads gate definitions from .novaforge/gates/ at `targetRef` — the branch being merged into — and unions them with the Work Item's required gates.

## Acceptance criteria

- [ ] Write the failing test `internal/gates/resolve_test.go`: `func TestResolveReadsFromTargetRefNotSource(t *testing.T)` stubs the git client so `main` declares gates `tests` and `security` while the source branch declares none, then asserts `Resolve` with `targetRef` `main` returns both — proving a change cannot delete the gates judging it; `func TestWorkItemGatesAreUnioned(t *testing.T)` asserts a Work Item requiring `api-compatibility` adds that gate even when .novaforge/gates/ omits it; `func TestUnknownGateNameRejected(t *testing.T)` asserts a definition named `nonsense` returns an error containing "unknown gate"; `func TestMalformedGateConfigFailsClosed(t *testing.T)` asserts invalid YAML returns an error and no definitions, so enforcement is never silently skipped. Run `go test ./internal/gates/` — expect FAIL with "undefined: gates.Resolve".
- [ ] Implement `Resolve` fetching only at `targetRef`, validating each name against the fixed set tests, architecture, security, api-compatibility, dependencies, quality, and documentation, and returning `fmt.Errorf("unknown gate %q", name)` for anything else.
- [ ] Run `go test ./internal/gates/` — expect PASS.
- [ ] Commit as `feat: resolve gate definitions from the target ref`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
