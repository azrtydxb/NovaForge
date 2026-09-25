# Teams (S-31)

Status: open
Created: 2026-09-25

## Description

The spec has claimed teams in S-1 and its Data section since the beginning and
nothing ever implemented them: there is no team table and no team code. This makes
the claim true rather than deleting it, and gives collaborators (task 3) something
to grant access to.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [ ] `internal/identity TestTeamAccess` passes: a team grants its members access to what
      the team is granted and nothing else, removing a person removes that access, and the
      team's role bounds its members regardless of their organization role.
- [ ] `teams` and `team_members` migrate, with `UNIQUE(org_id, name)`, a role CHECK
      matching organization roles, and cascade from both parents.
- [ ] Every team query carries the org predicate from `authz.FromContext`, proven by a
      test that another organization's teams are invisible.
- [ ] Routes under `/orgs/{org}/teams` exist and `make openapi` leaves no diff.
- [ ] The Orgs screen shows teams and their members; type-check and build clean.

## Evidence

<!-- Filled at close time. -->
