# applications 13: Applications e2e suite on kw, and gui/cli suite extensions

Status: open
Created: 2026-10-21

## Description

Task 13 of `.procoder/plans/applications.md` (spec `.procoder/specs/applications.md`): Applications e2e suite on kw, and gui/cli suite extensions. Done when the task's steps are carried out exactly as the plan writes them, its tests were seen red then green, and nothing outside the task's Files list changed except what its steps name.

## Acceptance criteria

- [ ] `tests/e2e/agent_test.sh` passes on kw through `hack/e2e-in-cluster.sh`.
- [ ] `tests/e2e/applications_test.sh` passes on kw through `hack/e2e-in-cluster.sh`.
- [ ] `tests/e2e/cli_test.sh` passes on kw through `hack/e2e-in-cluster.sh`.
- [ ] `tests/e2e/gui_test.sh` passes on kw through `hack/e2e-in-cluster.sh`.
- [ ] `tests/e2e/work_ci_test.sh` passes on kw through `hack/e2e-in-cluster.sh`.
- [ ] `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` (empty) and the procoder commit gate pass; the change is committed as `test(e2e): applications on the cluster`.

## Evidence

<!-- Filled at close time. -->
