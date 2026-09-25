# Repository administration (S-26)

Status: open
Created: 2026-09-25

## Description

A repository today has only GET and DELETE: it cannot be renamed, its default branch
cannot be changed, it cannot be archived and it cannot be transferred. Moving onto
NovaForge from another host means giving those up. Forks (task 8) need rename and
default branch, so this comes first.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [ ] `internal/gitops TestRepositoryAdministration` passes: rename then clone at the new
      name, change the default branch and a fresh clone checks it out, archive and a push
      is refused while a clone still succeeds, transfer and the repository belongs to the
      receiving organization and is gone from the sending one.
- [ ] A push to an archived repository is refused with a message naming the archive, not a
      generic denial, over both HTTP and SSH.
- [ ] `PATCH /orgs/{org}/repos/{repo}` and `POST /orgs/{org}/repos/{repo}/transfer` exist
      and `make openapi` leaves no diff.
- [ ] The Repos screen offers rename, default branch, archive and transfer, with archive
      shown as a state rather than a delete; `npx tsc -b --noEmit` and `npm run build`
      clean.
- [ ] `go test ./...` no failures and `gofmt -l internal cmd` prints nothing.

## Evidence

<!-- Filled at close time. -->
