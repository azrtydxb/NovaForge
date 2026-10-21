# applications 06: Merge sets — prepare, all-or-nothing commit, restore, revert, recovery

Status: open
Created: 2026-10-21

## Description

Task 6 of `.procoder/plans/applications.md` (spec `.procoder/specs/applications.md`): Merge sets — prepare, all-or-nothing commit, restore, revert, recovery. Done when the task's steps are carried out exactly as the plan writes them, its tests were seen red then green, and nothing outside the task's Files list changed except what its steps name.

## Acceptance criteria

- [ ] `TestMergeSetPublishesEveryTarget` passes against real datastores (`source hack/env.sh`), and was seen failing for the reason the plan names before the change.
- [ ] `TestMergeSetRestoresOnMovedTarget` passes against real datastores (`source hack/env.sh`), and was seen failing for the reason the plan names before the change.
- [ ] `TestMergeSetResumesAfterCrash` passes against real datastores (`source hack/env.sh`), and was seen failing for the reason the plan names before the change.
- [ ] The mutation steps in the plan's Task 6 were run: each named test went red with the mutation and green when restored.
- [ ] `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` (empty) and the procoder commit gate pass; the change is committed as `feat(gitops): all-or-nothing merge sets with a recoverable journal`.

## Evidence

<!-- Filled at close time. -->
