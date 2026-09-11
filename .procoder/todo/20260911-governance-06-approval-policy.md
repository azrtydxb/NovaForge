# governance 06: Approval policy

Status: open
Created: 2026-09-11

## Description

Plan step 6 of `.procoder/plans/governance.md`, which exists to: Make the platform, not the agent, the authority on merge: a gate controller that runs after an agent declares completion and cannot be bypassed or self-approved, a policy-driven approval model the model can never alter, and a secret broker issuing short-lived scoped credentials instead of durable secrets.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/approvals/policy.go`, `internal/approvals/policy_test.go`, `internal/approvals/migrations/000001_approvals.up.sql`, `internal/approvals/migrations/000001_approvals.down.sql`

Interfaces: produces `approvals.Action` as a string enum with the values `read_source`, `modify_workspace`, `add_dependency`, `change_db_schema`, `access_secret`, `deploy_staging`, and `deploy_production`; plus `approvals.Decide(ctx context.Context, p Policy, a Action, g capability.Grant) (Decision, error)` where `Decision` is one of `automatic`, `policy`, `human`, or `forbidden`, and `approvals.Store` with `Request`, `Resolve`, and `Pending`.

## Acceptance criteria

- [ ] Write the failing test `internal/approvals/policy_test.go`: `func TestReadSourceIsAutomatic(t *testing.T)` asserts `read_source` and `modify_workspace` both decide `automatic`; `func TestChangeDBSchemaRequiresArchitectureApproval(t *testing.T)` asserts `change_db_schema` decides `human`; `func TestDeployProductionForbiddenWithoutGrant(t *testing.T)` asserts a grant with `DeployProd == false` decides `forbidden` for `deploy_production`, and `human` when the grant allows it; `func TestUnknownActionIsForbidden(t *testing.T)` asserts an action outside the enum decides `forbidden` rather than defaulting open; `func TestPolicyIsNotDerivedFromModelOutput(t *testing.T)` asserts `Decide` takes no argument sourced from model output — the signature carries only a policy and a grant. Run `go test ./internal/approvals/` — expect FAIL with "undefined: approvals.Decide".
- [ ] Write the up migration creating `approval_requests` (id uuid pk, org_id uuid not null, run_id uuid not null, action text not null, detail jsonb not null, decision text not null default 'pending', decided_by uuid, decided_at timestamptz, created_at timestamptz not null default now()) and the matching down migration.
- [ ] Implement `Decide` as an exhaustive switch with a `default` returning `forbidden`, so a newly added action is denied until somebody writes its rule.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/approvals/` — expect PASS.
- [ ] Commit as `feat: add approval policy with deny-by-default decisions`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
