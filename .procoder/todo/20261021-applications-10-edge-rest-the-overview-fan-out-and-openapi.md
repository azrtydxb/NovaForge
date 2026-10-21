# applications 10: Edge REST, the overview fan-out, and OpenAPI

Status: open
Created: 2026-10-21

## Description

Task 10 of `.procoder/plans/applications.md` (spec `.procoder/specs/applications.md`): Edge REST, the overview fan-out, and OpenAPI. Done when the task's steps are carried out exactly as the plan writes them, its tests were seen red then green, and nothing outside the task's Files list changed except what its steps name.

## Acceptance criteria

- [ ] `TestOpenAPIListsEveryRoute` passes against real datastores (`source hack/env.sh`), and was seen failing for the reason the plan names before the change.
- [ ] `TestOverviewSaysUnavailableNotGreen` passes against real datastores (`source hack/env.sh`), and was seen failing for the reason the plan names before the change.
- [ ] `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` (empty) and the procoder commit gate pass; the change is committed as `feat(edge): application routes and the overview fan-out`.

## Evidence

<!-- Filled at close time. -->
