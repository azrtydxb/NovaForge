# Teams (S-31)

Status: closed 2026-09-25
Created: 2026-09-25

## Description

The spec has claimed teams in S-1 and its Data section since the beginning and
nothing ever implemented them: there is no team table and no team code. This makes
the claim true rather than deleting it, and gives collaborators (task 3) something
to grant access to.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [x] `internal/identity TestTeamAccess` passes: a team grants its members access to what
      the team is granted and nothing else, removing a person removes that access, and the
      team's role bounds its members regardless of their organization role.
- [x] `teams` and `team_members` migrate, with `UNIQUE(org_id, name)`, a role CHECK
      matching organization roles, and cascade from both parents.
- [x] Every team query carries the org predicate from `authz.FromContext`, proven by a
      test that another organization's teams are invisible.
- [x] Routes under `/orgs/{org}/teams` exist and `make openapi` leaves no diff.
- [x] The Orgs screen shows teams and their members; type-check and build clean.

## Evidence

- `go test ./internal/identity/ -run TestTeamAccess -v` — PASS with all six subtests
  running: a person in no team belongs to none; membership is reported; adding twice is
  not an error and does not duplicate; removal removes the access; a team's role is its
  own, not the organization's; listing is organization scoped. Seen to fail first with
  "store.CreateTeam undefined", so it failed for the missing implementation.
- The role subtest is the one that matters: the organization owner added to a team
  granting "member" is reported with role "member", not "owner". Reading the
  organization role there would make every team as powerful as its most privileged
  member, which is the opposite of what a narrower grant is for.
- Migration 000004 creates `teams` with `UNIQUE(org_id, name)` and a role CHECK matching
  the organization roles, and `team_members` with `PRIMARY KEY(team_id, user_id)` and
  cascade from both parents. The scoping subtest creates a "reviewers" team in a second
  organization and asserts this organization still sees exactly its own two.
- Every store query takes `org_id` directly; `team_members` carries `org_id` as well as
  `team_id` so a query can take the predicate without reaching it through a join a
  mistake could omit. `AddTeamMember` matches the team on organization and id together,
  so a caller cannot add someone to another organization's team by naming its id.
- Five routes under `/orgs/{org}/teams`; `make openapi` regenerated (+114 lines) and
  leaves no diff when run again.
- `npx tsc -b --noEmit` clean; `npm run build` succeeded (453.15 kB). The Orgs screen
  shows each team with the role it grants, its members as removable chips, and offers
  only organization members to add.
- `go test ./internal/identity/ ./internal/edge/` — both ok. `gofmt -l internal cmd`
  prints nothing.

Note on scope: `AddTeamMember` requires the person to already be an organization member
and `ListTeamsForUser` exists for git-platform to resolve a grant naming a team. Nothing
grants repository access to a team yet — that is task 3, which is what makes teams
load-bearing rather than decorative.
