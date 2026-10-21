# applications 01: Protos and migrations for applications and membership

Status: open
Created: 2026-10-21

## Description

Task 1 of `.procoder/plans/applications.md` (spec `.procoder/specs/applications.md`): Protos and migrations for applications and membership. Done when the task's steps are carried out exactly as the plan writes them, its tests were seen red then green, and nothing outside the task's Files list changed except what its steps name.

## Acceptance criteria

- [ ] `proto/novaforge/git/v1/git.proto` carries the application, release and merge-set messages and RPCs of the plan's Task 1, and `make generate` regenerates `gen/` with no other diff.
- [ ] Migrations 000010_applications, 000011_application_releases and 000012_merge_sets apply on a real database: `source hack/env.sh && go test ./internal/gitops/ -run TestCreateRepoInitialisesBareRepo -count=1` passes, and each `.down.sql` reverses its `.up.sql` (applied up, down, up on a scratch schema).
- [ ] `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` (empty) and the procoder commit gate pass; the change is committed as `feat(gitops): schema and API for applications, application releases and merge sets`.

## Evidence

<!-- Filled at close time. -->
