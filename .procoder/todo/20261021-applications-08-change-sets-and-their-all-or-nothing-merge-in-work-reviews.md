# applications 08: Change sets and their all-or-nothing merge in work-reviews

Status: open
Created: 2026-10-21

## Description

Task 8 of `.procoder/plans/applications.md` (spec `.procoder/specs/applications.md`): Change sets and their all-or-nothing merge in work-reviews. Done when the task's steps are carried out exactly as the plan writes them, its tests were seen red then green, and nothing outside the task's Files list changed except what its steps name.

## Acceptance criteria

- [ ] `TestChangeSetGroupsRunsOfOneApplication` passes against real datastores (`source hack/env.sh`), and was seen failing for the reason the plan names before the change.
- [ ] `TestMergeChangeSetRefusesWhenAnyRunIsBlocked` passes against real datastores (`source hack/env.sh`), and was seen failing for the reason the plan names before the change.
- [ ] `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` (empty) and the procoder commit gate pass; the change is committed as `feat(reviews): change sets merged all-or-nothing across an application`.

## Evidence

<!-- Filled at close time. -->
