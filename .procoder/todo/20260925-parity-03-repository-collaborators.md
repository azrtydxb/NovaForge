# Repository collaborators (S-26)

Status: closed 2026-09-25
Created: 2026-09-25

## Description

Access is organization-wide: there is no way to give one person access to one
repository. That forces an organization membership for anyone who needs to see
anything, which is coarser than what a team moving from Gitea expects.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [x] `internal/gitops TestRepositoryCollaborator` passes: a non-member granted one
      repository can clone and push it, cannot reach another repository in the same
      organization, and loses access when the grant is removed.
- [x] A grant may name a team instead of a person, resolved through Identity's RPC and not
      by reading its schema.
- [x] Collaborator access is consulted only after organization membership fails, so it is
      a second path to access and never a wider one.
- [x] Routes under `/orgs/{org}/repos/{repo}/collaborators` exist and `make openapi`
      leaves no diff.
- [x] The Repos screen manages collaborators; type-check and build clean.

## Evidence

- `go test ./cmd/git-platform/ -run TestOutsideCollaborator -v` — PASS with all five
  subtests: a non-member with no grant cannot clone; the granted repository clones and
  pushes with an unmodified git client; a second repository in the same organization is
  refused; a read grant cannot push, refused naming the read-only grant; and revoking
  removes the access. Driven over the real HTTP transport, not against the store.
- `go test ./internal/gitops/ -run TestRepositoryCollaboratorGrants -v` — nine subtests
  PASS, including that a grant names exactly one of a person or a team, that the stronger
  of two grants wins, that another organization's repository cannot be granted, and that
  an unavailable team lookup is an error rather than an empty set.
- `go test ./internal/authz/ -v` — `TestRepoLimitedScopeIsRefusedByRequireOrg` PASS: a
  repository-limited scope fails `RequireOrg`, `RequireRepo` admits only the granted
  repository and refuses it under another organization, a member is unaffected, and the
  role rides with the grant.
- Full `go test ./...` — **45 packages ok, 0 failures**. `gofmt -l internal cmd` prints
  nothing; `go build ./...` and `go vet ./...` clean. `make openapi` leaves no diff.
  `npx tsc -b --noEmit` clean and `npm run build` succeeded (463.65 kB).

Two defects this uncovered, both found only because the positive case was tested:

- **Reads were never authorized.** `CapFunc`'s contract says it is called with no refs
  for `git-upload-pack` "where it may still deny read access", and it was not: reads were
  gated by organization membership alone. A credential confined to one repository would
  have cloned every repository in the organization, over HTTP and over SSH. Both
  transports now authorize the read; authorizers that only care about writes already
  return early on an empty ref set, so nothing that worked before is denied.
- **A test passing for the wrong reason.** The first run failed the granted clone
  alongside the negative cases, because the team lookup presented no credential and so
  every collaborator failed to authenticate — the negative assertions were passing on a
  broken auth path. The lookup now presents git-platform's own service credential.

Security decisions recorded rather than assumed:

- `RequireOrg` refuses a repository-limited scope, so the 35 files that authorize on the
  organization alone fail closed without being edited. One missed call site would have
  been a data leak, not a bug.
- A collaborator can neither grant nor list grants: who else holds a repository is the
  organization's business, not the guest's.
- `ResolveUsername` is one exact match or NotFound, restricted to an owner/admin or an
  org-scoped service, because a looser lookup lets any authenticated account enumerate
  the deployment's users.

Not proven: nothing has run on the cluster. There is no e2e suite for collaborators and
no `nf` CLI support.
