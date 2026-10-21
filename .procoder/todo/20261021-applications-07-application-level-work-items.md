# applications 07: Application-level Work Items

Status: open
Created: 2026-10-21

## Description

Task 7 of `.procoder/plans/applications.md` (spec `.procoder/specs/applications.md`): Application-level Work Items. Done when the task's steps are carried out exactly as the plan writes them, its tests were seen red then green, and nothing outside the task's Files list changed except what its steps name.

## Acceptance criteria

- [ ] `TestApplicationWorkItemHasNoRepository` passes against real datastores (`source hack/env.sh`), and was seen failing for the reason the plan names before the change.
- [ ] The mutation steps in the plan's Task 7 were run: each named test went red with the mutation and green when restored.
- [ ] `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` (empty) and the procoder commit gate pass; the change is committed as `feat(work): Work Items that belong to an application`.

## Evidence

<!-- Filled at close time. -->
