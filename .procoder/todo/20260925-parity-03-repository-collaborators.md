# Repository collaborators (S-26)

Status: open
Created: 2026-09-25

## Description

Access is organization-wide: there is no way to give one person access to one
repository. That forces an organization membership for anyone who needs to see
anything, which is coarser than what a team moving from Gitea expects.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [ ] `internal/gitops TestRepositoryCollaborator` passes: a non-member granted one
      repository can clone and push it, cannot reach another repository in the same
      organization, and loses access when the grant is removed.
- [ ] A grant may name a team instead of a person, resolved through Identity's RPC and not
      by reading its schema.
- [ ] Collaborator access is consulted only after organization membership fails, so it is
      a second path to access and never a wider one.
- [ ] Routes under `/orgs/{org}/repos/{repo}/collaborators` exist and `make openapi`
      leaves no diff.
- [ ] The Repos screen manages collaborators; type-check and build clean.

## Evidence

<!-- Filled at close time. -->
