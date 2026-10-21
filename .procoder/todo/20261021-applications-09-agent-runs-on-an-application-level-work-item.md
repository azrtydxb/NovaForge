# applications 09: Agent Runs on an application-level Work Item

Status: open
Created: 2026-10-21

## Description

Task 9 of `.procoder/plans/applications.md` (spec `.procoder/specs/applications.md`): Agent Runs on an application-level Work Item. Done when the task's steps are carried out exactly as the plan writes them, its tests were seen red then green, and nothing outside the task's Files list changed except what its steps name.

## Acceptance criteria

- [ ] `TestAgentPushOutsideGrantRepos` passes against real datastores (`source hack/env.sh`), and was seen failing for the reason the plan names before the change.
- [ ] `TestApplicationRunGrantNamesEveryMember` passes against real datastores (`source hack/env.sh`), and was seen failing for the reason the plan names before the change.
- [ ] `tests/e2e/applications_test.sh` passes on kw through `hack/e2e-in-cluster.sh`.
- [ ] `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` (empty) and the procoder commit gate pass; the change is committed as `feat(agents): Agent Runs across an application, confined to its repositories`.

## Evidence

<!-- Filled at close time. -->
