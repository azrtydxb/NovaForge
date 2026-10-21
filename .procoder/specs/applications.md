# applications

Status: complete

## Problem

A product built as microservices lives in several repositories, but NovaForge
shows each repository on its own. Nothing answers "what is the state of my
application": which services are green, what is released, what changed across
them since the last release. A change that touches three services is three
unrelated Engineering Runs, three reviews and three merges, with nothing saying
they belong together or must land together, so the application can sit
half-changed. NovaForge is AI-native, which makes this worse than elsewhere: an
agent implementing one feature across three services produces three
uncoordinated changes where a reviewer needs one.

## Users

- **Developer**: sees the application as one thing. That means its services
  and their CI state, open changes, releases, recent activity, and search
  across all of them.
- **Reviewer**: reviews a change that spans several services as one unit, and
  merges it knowing it lands everywhere or nowhere.
- **Agent** (Agent Run on an application-level Work Item): works across the
  member repositories and may write only to them.
- **Org member who arranges**: creates applications, adds and removes
  repositories, and cuts releases.

## Decisions taken in the interview (2026-10-21)

- An application groups **separate repositories** (polyrepo), not folders of
  one repository.
- A repository belongs to **at most one** application.
- The first version includes the grouped view, composite releases and
  cross-repository changes.
- Access **stays per repository**. An application shows only the members the
  viewer can read, and says how many are hidden.
- A cross-repository change merges **all-or-nothing**.
- A release **records** each member's commit and **tags** each member
  repository.

## In scope

- [S-1] Applications as org-scoped objects (name, description), with member
  repositories (each repository in at most one). Members can be created,
  renamed, deleted, added and removed.
- [S-2] An application overview. For each member: its default branch's latest
  CI state, open Engineering Runs, its latest release tag and recent commits.
  An overall state is the worst of the members. Members the viewer cannot
  read are hidden and counted.
- [S-3] Search across an application: code and knowledge search over every
  readable member, ranked together.
- [S-4] Composite releases. A release `<version>` records one commit per
  member and pushes an annotated tag `<application>/<version>` into every
  member, all-or-nothing. Releases can be listed and shown, and two releases
  can be compared, giving the commit range per member.
- [S-5] Application-level Work Items: a Work Item that belongs to an
  application instead of one repository.
- [S-6] Change sets: one cross-repository change that groups one Engineering
  Run per touched member, belongs to an application, and optionally links a
  Work Item. It is viewed and reviewed as one unit.
- [S-7] All-or-nothing merge of a change set. Every run must be mergeable
  (gates pass at its head, independent approval at its head, target
  unchanged) before any merge. The repositories are then merged, and on a
  failure part-way the ones already merged are restored. The progress is
  journalled, so a crash resumes the merge or the rollback.
- [S-8] Agent Runs on an application-level Work Item. The workspace holds
  every member the sponsor may write. The agent's grant names those
  repositories, so it cannot write any other repository even under its
  branch prefix. Its result is a change set.
- [S-9] Repository-scoped grants: `capability.Grant` gains the set of
  repositories it applies to, and every new Agent Run, single-repository
  included, is issued a grant naming its repositories.
- [S-10] Fullstack surfaces: REST at the edge, `nf app ...` in the CLI, and
  in the GUI an "Applications" entry under BUILD, an application page (with
  Overview, Repositories, Changes, Releases, Search and Work tabs), and
  "Part of `<application>`" on a member's repository page.

## Out of scope

- Git submodules or a monorepo split by path. A repository's history changes
  only through its normal merges and through the release tags of S-4.
- An application spanning organizations, or a repository in several
  applications.
- Code graph queries across repositories (dependents/dependencies through
  service calls between members). Graph queries stay per repository; only
  search (S-3) spans the application.
- Deploying an application as a unit: governed deployments stay per
  repository and may reference a release later.
- Access control at the application level; a grant on an application.
- Migrating existing per-repository releases into application releases.

## Constraints

- Organizations are a hard boundary. Every query carries the org from
  `authz.FromContext`. An application and all its members are in one org, and
  a repository transferred to another org leaves its application.
- One schema per service, and no cross-schema reads. git-platform owns
  applications, membership and application releases (they need the
  repositories and git). work-reviews owns change sets and calls git-platform
  over RPC to validate membership and to merge.
- Git is the `git` binary. Writes happen in throwaway clones, and refs are
  published with `git update-ref --stdin`, verifying old values, as `Merge`
  does today.
- An application holds at most 100 repositories. The overview fans out with
  bounded concurrency (8). It answers within 3 s for 20 members on kw, and
  says "unavailable" for a member whose data did not arrive in time rather
  than leaving it out silently.
- Capability grants constrain agents, not human members. A human merges a
  change set with ordinary write access to each repository in it.
- A screen says when the deployment cannot answer, and never renders invented
  data.
- MCP, CLI, REST and GUI ship together for each capability.

## Interfaces

**gRPC: git-platform** (`novaforge.git.v1.GitService`):

- `CreateApplication`, `GetApplication`, `ListApplications`,
  `UpdateApplication`, `DeleteApplication`.
- `SetRepoApplication(repo_id, application_id | empty)`: refused when the
  repository is in another application, unless `move=true`.
- `CreateApplicationRelease(application_id, version, name, notes, pins[]?)`,
  where a pin is `{repo_id, commit_sha}` and an absent pin means the
  default-branch head. Also `ListApplicationReleases`,
  `GetApplicationRelease` and `CompareApplicationReleases(from, to)`, which
  returns per member `{repo, from_sha, to_sha, commits[]}`.
- `PrepareMergeSet(items[] {repo_id, source_ref, target_ref, method,
expected_source_sha, expected_target_sha}) → candidates[] {repo_id,
merge_sha}`. It builds each merge commit in a throwaway clone and stores it
  under `refs/novaforge/mergesets/<id>/<repo>`, without publishing.
- `CommitMergeSet(id)` publishes each target, verifying the expected old
  value. On a failure it restores the targets already moved, and it returns
  per-repository outcomes.

**gRPC: work-reviews** (`novaforge.reviews.v1.ReviewsService`):
`CreateChangeSet(application_id, title, work_item_key?, run_ids[])`,
`GetChangeSet`, `ListChangeSets(application_id, state?)`,
`AddRunToChangeSet`, `MergeChangeSet(id, method)`.

**gRPC: work, agents and graph:**

- `WorkItem` gains `application_id`; `repo_id` may be empty when it is set.
- `StartRunRequest` gains `application_id`, as an alternative to `repo_id`.
- `SearchCodeRequest` and `SearchKnowledgeRequest` gain `repeated repo_ids`;
  results carry `repo_id`.

**REST at the edge**, all under `/api/v1/orgs/{org}`:

- `GET|POST /apps`, `GET|PATCH|DELETE /apps/{app}`
- `PUT|DELETE /apps/{app}/repos/{repo}`
- `GET /apps/{app}/overview`, `GET /apps/{app}/search?q=&kind=code|knowledge`
- `GET|POST /apps/{app}/releases`, `GET /apps/{app}/releases/{version}`,
  `GET /apps/{app}/releases/compare?from=&to=`
- `GET|POST /apps/{app}/work`, `GET /apps/{app}/work/{key}`
- `GET|POST /apps/{app}/changes`, `GET /apps/{app}/changes/{id}`,
  `POST /apps/{app}/changes/{id}/runs`, `POST /apps/{app}/changes/{id}/merge`
- `POST /apps/{app}/agent-runs`
- `api/openapi.yaml` is regenerated from the route table.

**CLI:**

- `nf app create|list|show|rename|delete`
- `nf app add|remove <app> <repo>`
- `nf app status <app>`
- `nf app search <app> <query>`
- `nf app release create|list|show|compare`
- `nf app change create|list|show|merge`
- `nf app work create|list`

**GUI** (one file per screen, under `web/src/screens/`):

- An applications list screen, and an application page screen with tabs
  Overview, Repositories, Changes, Releases, Search and Work.
- The Rail gains "Applications" under BUILD.
- The repository page header shows "Part of `<app>`".
- A change set page lists each repository's run, gate state and approval,
  with one Merge button. The button is enabled only when every run is
  mergeable, and otherwise names what blocks it.

## Data

**git-platform schema** (`gitplatform`, new migrations):

- `applications(id uuid PK, org_id uuid NOT NULL, name text NOT NULL,
description text NOT NULL DEFAULT '', created_by uuid, created_at
timestamptz, UNIQUE(org_id, name))`
- `repositories.application_id uuid NULL REFERENCES applications(id) ON
DELETE SET NULL`, with an index. A CHECK by trigger keeps the application
  in the repository's org.
- `application_releases(id uuid PK, org_id, application_id FK ON DELETE
CASCADE, version text, name text, notes text, created_by uuid, created_at,
UNIQUE(application_id, version))`
- `application_release_members(release_id FK ON DELETE CASCADE, repo_id uuid
NULL ON DELETE SET NULL, repo_name text NOT NULL, commit_sha text NOT NULL,
tag text NOT NULL, PRIMARY KEY(release_id, repo_name))`. `repo_name` is a
  snapshot, so a release still reads after a member is deleted.
- `merge_sets(id uuid PK, org_id, state text CHECK in (prepared, committing,
committed, rolling_back, rolled_back, failed), created_at, updated_at)`
- `merge_set_items(set_id FK, repo_id, target_ref, expected_target_sha,
merge_sha, state text CHECK in (pending, published, restored, conflict),
PRIMARY KEY(set_id, repo_id))`. This is the journal a crash resumes from.

**work-reviews schema:**

- `change_sets(id uuid PK, org_id, application_id uuid NOT NULL, number int,
title, work_item_id NULL, state text CHECK in (open, merging, merged,
closed, failed), merge_set_id NULL, failure text NULL, created_by,
created_at, UNIQUE(application_id, number))`
- `runs.change_set_id uuid NULL REFERENCES change_sets(id)`. A run is in at
  most one change set, and a change set holds at most one run per repository.

**work schema:** `work_items.application_id uuid NULL`. A Work Item has
exactly one of `repo_id` and `application_id`.

**capability:** `capability_grants.repo_ids uuid[] NULL`. NULL means "every
repository in the org", which stays only for grants issued before this
change; new grants always set it.

## Edge cases

- **A member is deleted:** it leaves the application (`SET NULL`). Its
  release members keep the recorded name and commit. A change set with a run
  on it cannot merge, and says so.
- **A member is archived:** it is shown as archived and kept out of new
  releases' default pins. A change set touching it cannot merge (`Merge`
  already refuses archived repositories).
- **A member is renamed:** applications reference the id. Release tags
  already pushed keep their names.
- **A member is transferred to another org:** it leaves the application in
  the same transaction.
- **A repository is added while it is in another application:** refused,
  naming that application, unless the caller asked to move it.
- **A release whose tag already exists in a member:** a tag pointing at the
  pinned commit is reused. A tag pointing elsewhere refuses the whole release
  before any tag is written.
- **A release with a member that has no commits:** that member is left out
  and named in the response.
- **The target moves between prepare and commit:** that repository's publish
  fails its old-value check, and the repositories already published are
  restored.
- **Restoring finds someone already pushed on top of our merge commit:** the
  merge cannot be undone by a ref reset. A revert commit of our merge is
  pushed on top instead, and the change set records which repository needed
  one.
- **A gate or approval goes stale between check and merge:** the gates are
  re-evaluated at the pinned heads (`MayMergePinned`), and the pinned source
  SHAs are what merges.
- **A change set mixes repositories the merger cannot write:** refused before
  prepare. A repository the caller cannot read is reported as "a repository
  you cannot access", not by name.
- **An application with no members or no readable members:** the overview
  says so; it is not an error.
- **The org is deleted:** its applications, releases and change sets go with
  it (the existing `RepoDeletedEvent` / org-deletion cleanup is extended).
- **An agent's workspace for an application with 100 members:** every member
  the sponsor may write is cloned, shallow (depth 50), within the workspace
  quota. When the quota is exceeded, the run fails saying so.

## Failure modes

- **git-platform unavailable:** application screens fail with the
  deployment's normal "request failed", never an empty list.
- **CI unavailable or slow for one member:** that member's CI state is
  "unavailable", and the overall state is "unknown", not green.
- **graph unavailable:** application search says search is unavailable. A
  member not yet indexed is listed as "not indexed".
- **git-platform crashes mid-merge:** the `merge_sets` journal says which
  repositories were published. On start, git-platform resumes every set in
  `committing` or `rolling_back`. It finishes the rollback unless every item
  was published, in which case the set is `committed`. work-reviews
  reconciles its change set from `GetMergeSet`.
- **work-reviews crashes after CommitMergeSet:** on start it reconciles every
  change set in `merging` against its merge set.
- **Tag push fails for one member during a release:** the tags already
  pushed are deleted, and the release is not recorded.
- **gates unavailable:** a change set is not mergeable, and says gates could
  not be evaluated.

## Acceptance criteria

- [ ] [S-1] `TestApplicationMembershipIsExclusiveAndOrgScoped` (internal/gitops,
      real PostgreSQL): an application is created, renamed and deleted; adding
      a repository already in another application is refused naming it and
      succeeds with move; a repository of another org cannot be added. Fails if
      the membership is not exclusive or the org predicate comes from the
      request.
- [ ] [S-1] `TestMemberLeavesApplicationOnDeleteAndTransfer` (internal/gitops):
      deleting or transferring a member clears its membership, and deleting the
      application leaves its repositories. Fails if the foreign key or the
      transfer path does not clear `application_id`.
- [ ] [S-2] `TestApplicationOverviewOnTheCluster`, run on kw by the new
      applications e2e suite: the overview of a 3-member
      application shows each member's default-branch CI state from real runs,
      open runs and latest tag; with one member unreadable to the viewer it is
      absent and "1 hidden" is shown. `TestOverviewSaysUnavailableNotGreen`
      (internal/edge) asserts that a member whose CI call fails reads
      "unavailable" and the overall state "unknown". Fails if a failed member
      is dropped or counted as green.
- [ ] [S-3] `TestApplicationSearchSpansOnlyMembers` (internal/graph, real
      PostgreSQL with pgvector), and the same on kw in a new applications e2e
      suite: two symbols pushed to two members
      are both found by one application search, each labelled with its
      repository, and a repository outside the application is not searched.
      Fails if search ignores `repo_ids` or searches the whole org.
- [ ] [S-4] `TestApplicationReleaseTagsEveryMemberOrNone` (internal/gitops, real
      bare repositories): release `1.0.0` of 3 members records 3 commits and
      `git ls-remote --tags` shows `<app>/1.0.0` at each; with a conflicting tag
      planted in one member the release is refused and no member gains a tag.
      Fails if tags are written before every member is checked.
- [ ] [S-4] `TestCompareApplicationReleases` (internal/gitops): comparing
      `1.0.0` with `1.1.0` lists, per member, exactly the commits between the
      recorded SHAs. Fails if the range is taken from current heads.
- [ ] [S-5] [S-6] `TestChangeSetGroupsRunsOfOneApplication` (internal/reviews,
      real PostgreSQL): an application-level Work Item is created, runs on two
      members join one change set, a run of a non-member is refused, and a
      second run on the same repository is refused. Fails if membership is not
      checked through git-platform.
- [ ] [S-7] `TestMergeSetPublishesEveryTarget` (internal/gitops): a set over two
      approved, green members advances both targets to their merge commits.
      Fails if either target is left behind.
- [ ] [S-7] `TestMergeChangeSetRefusesWhenAnyRunIsBlocked` (internal/reviews):
      with one member unapproved or its gate failing, nothing is prepared or
      published and the response names the repository and reason. Fails if any
      repository is merged first.
- [ ] [S-7] `TestMergeSetRestoresOnMovedTarget` (internal/gitops): with the
      second target moved after prepare, the first target is restored to its
      prior SHA, the second is untouched, and the set is `rolled_back` naming
      the second. Fails if the first merge stays published.
- [ ] [S-7] `TestMergeSetResumesAfterCrash` (internal/gitops): a set journalled
      as `committing` with one item published is resumed by a fresh process and
      ends with refs and journal consistent. Fails if recovery is not run at
      start.
- [ ] [S-8] `TestApplicationAgentRunCannotPushOutsideMembers`, run on kw by
      the new applications e2e suite: an Agent Run on an
      application-level Work Item commits to two members and produces a change
      set, and its push under its branch prefix to a third repository of the
      same org is refused by git-platform. Fails if the grant omits
      `repo_ids`.
- [ ] [S-9] `TestGrantRefusesRepositoryOutsideItsSet` (internal/capability): a
      grant naming repository A refuses a write to B under the same prefix, and
      `TestSingleRepoRunGrantNamesItsRepo` (internal/agents) asserts the issued
      grant names only the run's repository. Fails if `repo_ids` is not checked
      on write.
- [ ] [S-10] `tests/e2e/gui_test.sh` and `tests/e2e/cli_test.sh` extended: every
      new route answers from the live API on kw, the GUI's Applications list,
      application tabs, change set page and "Part of" header render, `nf app`
      covers each operation, and `TestOpenAPIListsEveryRoute` passes after
      `make openapi`. Fails if a route has no screen or CLI command.

## Open questions
