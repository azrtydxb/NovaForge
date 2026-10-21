# applications 05: Application releases, tags and compare

Status: open
Created: 2026-10-21

## Description

Task 5 of `.procoder/plans/applications.md` (spec `.procoder/specs/applications.md`): Application releases, tags and compare. Done when the task's steps are carried out exactly as the plan writes them, its tests were seen red then green, and nothing outside the task's Files list changed except what its steps name.

## Acceptance criteria

- [ ] `TestApplicationReleaseTagsEveryMemberOrNone` passes against real datastores (`source hack/env.sh`), and was seen failing for the reason the plan names before the change.
- [ ] `TestCompareApplicationReleases` passes against real datastores (`source hack/env.sh`), and was seen failing for the reason the plan names before the change.
- [ ] `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` (empty) and the procoder commit gate pass; the change is committed as `feat(gitops): composite application releases tagged in every member`.

## Evidence

<!-- Filled at close time. -->
