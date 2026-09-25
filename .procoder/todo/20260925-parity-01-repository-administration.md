# Repository administration (S-26)

Status: closed 2026-09-25
Created: 2026-09-25

## Description

A repository today has only GET and DELETE: it cannot be renamed, its default branch
cannot be changed, it cannot be archived and it cannot be transferred. Moving onto
NovaForge from another host means giving those up. Forks (task 8) need rename and
default branch, so this comes first.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [x] `internal/gitops TestRepositoryAdministration` passes: rename then clone at the new
      name, change the default branch and a fresh clone checks it out, archive and a push
      is refused while a clone still succeeds, transfer and the repository belongs to the
      receiving organization and is gone from the sending one.
- [x] A push to an archived repository is refused with a message naming the archive, not a
      generic denial, over both HTTP and SSH.
- [x] `PATCH /orgs/{org}/repos/{repo}` and `POST /orgs/{org}/repos/{repo}/transfer` exist
      and `make openapi` leaves no diff.
- [x] The Repos screen offers rename, default branch, archive and transfer, with archive
      shown as a state rather than a delete; `npx tsc -b --noEmit` and `npm run build`
      clean.
- [x] `go test ./...` no failures and `gofmt -l internal cmd` prints nothing.

## Evidence

- `go test ./internal/gitops/ -run TestRepositoryAdministration -v` — PASS with all four
  subtests running: rename, default branch, archive, transfer. Seen to fail first with
  "srv.UpdateRepo undefined", and again on two field names my test had guessed wrong
  (`GetRepoRequest.Repo`, `MergeRequest.Base`), so it failed for the missing RPC and then
  for using the real API.
- `go test ./cmd/git-platform/ -run TestArchivedRepositoryRefusesAPush` — PASS. An
  unmodified git client pushes before archiving and is refused after, naming the archive,
  and a clone still succeeds. Red-green proven: with the archive guard unwired from
  `newCapFunc` the test fails with "an archived repository accepted a push", which is the
  seam this repository keeps hitting — a rule the RPC enforces and the transports do not.
- `make openapi` regenerated `api/openapi.yaml` with the two new routes (+47 lines) and
  leaves no diff when run again.
- `npx tsc -b --noEmit` clean; `npm run build` succeeded (449.19 kB). The Repos screen
  offers rename, default branch chosen from the repository's own branches, archive as a
  state to move in and out of, and transfer limited to organizations the person belongs to.
- `go test ./internal/gitops/ ./internal/edge/ ./cmd/git-platform/` — all ok.
  `gofmt -l internal cmd` prints nothing.
- Full `go test ./...` — 42 ok. Two failures in that run were not this change:
  `internal/gates` failed on "no migration found for version 4" because the run had
  compiled while the next task's identity migration was being written, and passes on
  re-run; `internal/spectrace` failed because the parity criteria had no traceability
  entries yet, and passes now that they do (37 covered, 10 uncovered of 47).
