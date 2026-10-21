# applications 11: `nf app` in the CLI

Status: open
Created: 2026-10-21

## Description

Task 11 of `.procoder/plans/applications.md` (spec `.procoder/specs/applications.md`): `nf app` in the CLI. Done when the task's steps are carried out exactly as the plan writes them, its tests were seen red then green, and nothing outside the task's Files list changed except what its steps name.

## Acceptance criteria

- [ ] `TestAppCommandsCallTheirRoutes` passes against real datastores (`source hack/env.sh`), and was seen failing for the reason the plan names before the change.
- [ ] `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` (empty) and the procoder commit gate pass; the change is committed as `feat(cli): nf app`.

## Evidence

<!-- Filled at close time. -->
