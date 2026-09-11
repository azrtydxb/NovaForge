# factory 06: Proposing maintenance Work Items

Status: open
Created: 2026-09-11

## Description

Plan step 6 of `.procoder/plans/factory.md`, which exists to: Close the loop from AI coding assistant to AI software engineering organization: decompose an epic into dependency-ordered subtasks across specialized agents, review changes with independent agents rather than their author, detect maintenance work autonomously and propose it as Work Items, and auto-merge only where policy already allows it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/maintenance/propose.go`, `internal/maintenance/propose_test.go`

Interfaces: produces `maintenance.Proposer.Propose(ctx context.Context, orgID, repoID uuid.UUID, findings []Finding) ([]work.Item, error)` creating Work Items in state `open` with `assignee_kind` unset, and `maintenance.Proposer.Run(ctx context.Context) error` scanning every repository on a 24h ticker.

## Acceptance criteria

- [ ] Write the failing test `internal/maintenance/propose_test.go`: `func TestProposalCreatesUnassignedWorkItem(t *testing.T)` asserts a CVE finding yields a Work Item of type `security` in state `open` with no assignee — nothing is executed unapproved; `func TestDuplicateFindingDoesNotDuplicateWorkItem(t *testing.T)` proposes the identical finding twice and asserts one Work Item exists; `func TestResolvedFindingClosesProposal(t *testing.T)` proposes a finding, then rescans without it, and asserts the Work Item is moved to `done` with a note rather than deleted; `func TestProposalNeverStartsAnAgentRun(t *testing.T)` asserts no Agent Run exists for any proposed item after `Propose` returns. Run `go test ./internal/maintenance/` — expect FAIL with "undefined: maintenance.Proposer".
- [ ] Implement deduplication with a stable fingerprint of `kind` plus sorted `Paths` plus `Title`, stored on the Work Item and enforced by a unique index.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/maintenance/` — expect PASS.
- [ ] Commit as `feat: propose maintenance work items without executing them`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
