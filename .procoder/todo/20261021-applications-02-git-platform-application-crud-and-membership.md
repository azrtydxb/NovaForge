# applications 02: git-platform application CRUD and membership

Status: open
Created: 2026-10-21

## Description

Task 2 of `.procoder/plans/applications.md` (spec `.procoder/specs/applications.md`): git-platform application CRUD and membership. Done when the task's steps are carried out exactly as the plan writes them, its tests were seen red then green, and nothing outside the task's Files list changed except what its steps name.

## Acceptance criteria

- [ ] `TestApplicationMembershipIsExclusiveAndOrgScoped` passes against real datastores (`source hack/env.sh`), and was seen failing for the reason the plan names before the change.
- [ ] `TestMemberLeavesApplicationOnDeleteAndTransfer` passes against real datastores (`source hack/env.sh`), and was seen failing for the reason the plan names before the change.
- [ ] The mutation steps in the plan's Task 2 were run: each named test went red with the mutation and green when restored.
- [ ] `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` (empty) and the procoder commit gate pass; the change is committed as `feat(gitops): applications and exclusive, org-scoped membership`.

## Evidence

<!-- Filled at close time. -->
