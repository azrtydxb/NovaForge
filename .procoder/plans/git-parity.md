# git-parity — implementation plan

Status: draft
Spec: .procoder/specs/backend-platform.md

## Goal

Make NovaForge a self-hosted Git server a team can move onto from Gitea without giving up
what they already rely on: S-24 to S-31.

## Architecture

Every feature here extends an existing service rather than adding one. git-platform owns
repository identity, so administration, forks, import/mirroring, LFS and HTTPS land there
(`internal/gitops`, schema `gitplatform`). Webhooks consume the Redis streams that already
carry pushes, runs and CI results, and are delivered by a worker in git-platform. Releases
and LFS objects go in the existing blobstore (`internal/blobstore`), keyed so an object is
reachable only inside the organization that owns its repository. Teams extend identity
(`internal/identity`), and repository collaborators extend `gitplatform` because that is
where repository access is decided.

Tasks are ordered by dependency: administration before forks (a fork needs rename and
default branch), teams and collaborators before anything that authorizes on them.

## Constraints

Copied from the spec and CLAUDE.md; every task inherits these.

- Organizations are a hard security boundary. Every query carries an org predicate taken
  from `authz.FromContext`, never from a request field.
- One PostgreSQL cluster, one schema per service, no cross-schema reads. A service needing
  another's data calls its RPC. `database.Migrate(url, schema, fs)` owns migrations.
- Git is the `git` binary, shelled out to. Never go-git. Write operations happen in a
  throwaway clone that is pushed back, so a failure part-way leaves the bare repo untouched.
- Capability grants constrain agents, not human members.
- REST exists only at the edge, generated from the route table; run `make openapi` after
  adding routes.
- Write the failing test first and see it fail for the right reason.
- Secrets are never returned by a read, and never appear in a log.
- The GUI ships with the feature: a backend route with no screen is not done (project
  convention, see CLAUDE.md on the embedded web application).

## Task 1: Repository administration

Files: `internal/gitops/admin.go`, `internal/gitops/admin_test.go`,
`internal/gitops/migrations/00000N_repo_admin.up.sql` and `.down.sql`,
`internal/gitops/grpc.go`, `proto/novaforge/git/v1/git.proto`,
`internal/edge/routes.go`, `internal/edge/repo_handlers.go`, `web/src/screens/Repos.tsx`

Interfaces: produces
`(s *Store) RenameRepo(ctx context.Context, id uuid.UUID, name string) error`,
`(s *Store) SetDefaultBranch(ctx context.Context, id uuid.UUID, branch string) error`,
`(s *Store) SetArchived(ctx context.Context, id uuid.UUID, archived bool) error`,
`(s *Store) TransferRepo(ctx context.Context, id, toOrg uuid.UUID) error`;
RPCs `UpdateRepo(UpdateRepoRequest) returns (UpdateRepoResponse)` and
`TransferRepo(TransferRepoRequest) returns (TransferRepoResponse)`.

- [ ] Write the failing test `internal/gitops/admin_test.go`:
      `func TestRepositoryAdministration(t *testing.T)` creates a repository, pushes a commit,
      renames it and clones at the new name, sets the default branch to a second branch and
      asserts a fresh clone checks that branch out, archives it and asserts a push is refused
      while a clone still succeeds, then transfers it and asserts it is listed in the receiving
      organization and absent from the sending one. `expect FAIL with "undefined: RenameRepo"`.
- [ ] Add the migration: `ALTER TABLE repositories ADD COLUMN archived boolean NOT NULL
DEFAULT false;` and a unique index on `(org_id, name)` if one is not already present, so
      a rename cannot collide.
- [ ] Implement the four store methods. A rename moves the bare repository directory with the
      database row in one transaction, and rolls the directory back if the commit fails — the
      repository's path is derived from its id, not its name, if `resolvePath` allows it; check
      `internal/gitops/repo.go` first and prefer id-derived paths so a rename touches no files.
- [ ] Refuse a push to an archived repository in `internal/gitops/http.go` and `ssh.go` with a
      message naming the archive, not a generic denial.
- [ ] Add the RPCs and edge routes `PATCH /api/v1/orgs/{org}/repos/{repo}` and
      `POST /api/v1/orgs/{org}/repos/{repo}/transfer`; run `make openapi`.
- [ ] Add the controls to `web/src/screens/Repos.tsx`, with archive shown as a state rather
      than a delete.
- [ ] Commit as `feat(git): administer a repository — rename, default branch, archive, transfer`.

## Task 2: Teams

Files: `internal/identity/teams.go`, `internal/identity/teams_test.go`,
`internal/identity/migrations/00000N_teams.up.sql` and `.down.sql`,
`internal/identity/grpc.go`, `proto/novaforge/identity/v1/identity.proto`,
`internal/edge/routes.go`, `internal/edge/org_handlers.go`, `web/src/screens/Orgs.tsx`

Interfaces: produces `identity.Team{ID, OrgID uuid.UUID; Name, Role string}`,
`(s *Store) CreateTeam`, `(s *Store) AddTeamMember`, `(s *Store) RemoveTeamMember`,
`(s *Store) ListTeams`, `(s *Store) TeamsForUser(ctx, orgID, userID) ([]Team, error)`;
RPC `ListTeams`, `CreateTeam`, `AddTeamMember`, `RemoveTeamMember`.

- [ ] Write the failing test `internal/identity/teams_test.go`:
      `func TestTeamAccess(t *testing.T)` creates a team with role `member`, adds a person,
      asserts `TeamsForUser` returns it, removes them and asserts it does not, and asserts a
      team's role bounds the person's effective role even when their organization role is
      higher. `expect FAIL with "undefined: CreateTeam"`.
- [ ] Add the migration: `teams(id, org_id, name, role)` with `UNIQUE(org_id, name)` and a
      role CHECK matching the organization roles, and `team_members(org_id, team_id, user_id)`
      with `PRIMARY KEY(team_id, user_id)` and `ON DELETE CASCADE` from both.
- [ ] Implement the store methods, every query carrying the org predicate from
      `authz.FromContext`.
- [ ] Add the RPCs, the edge routes under `/api/v1/orgs/{org}/teams`, and `make openapi`.
- [ ] Show teams and their members on `web/src/screens/Orgs.tsx`.
- [ ] Commit as `feat(identity): teams the spec has always claimed`.

## Task 3: Repository collaborators

Files: `internal/gitops/collaborators.go`, `internal/gitops/collaborators_test.go`,
`internal/gitops/migrations/00000N_collaborators.up.sql` and `.down.sql`,
`internal/gitops/grpc.go`, `internal/gitops/http.go`, `internal/gitops/ssh.go`,
`proto/novaforge/git/v1/git.proto`, `internal/edge/routes.go`, `web/src/screens/Repos.tsx`

Interfaces: consumes `identity.TeamsForUser` through the Identity RPC; produces
`(s *Store) AddCollaborator(ctx, repoID, userID uuid.UUID, role string) error`,
`(s *Store) RemoveCollaborator`, `(s *Store) ListCollaborators`,
`(s *Store) MayAccess(ctx, repoID, userID uuid.UUID) (string, bool, error)`.

- [ ] Write the failing test `internal/gitops/collaborators_test.go`:
      `func TestRepositoryCollaborator(t *testing.T)` grants a non-member access to one
      repository, asserts they can clone and push it, asserts they cannot reach a second
      repository in the same organization, and asserts removing the grant removes access.
      `expect FAIL with "undefined: AddCollaborator"`.
- [ ] Add the migration: `repository_collaborators(org_id, repo_id, user_id, role)` with
      `PRIMARY KEY(repo_id, user_id)` and a role CHECK, plus `team_id` nullable so a grant may
      name a team instead of a person.
- [ ] Extend the transport authorization in `http.go` and `ssh.go` to consult `MayAccess`
      after organization membership fails, so a collaborator is a second path to access and
      never a wider one.
- [ ] Add the RPCs, edge routes under `/api/v1/orgs/{org}/repos/{repo}/collaborators`, and
      `make openapi`.
- [ ] Commit as `feat(git): grant access to one repository, to a person or a team`.

## Task 4: Webhooks

Files: `internal/webhooks/store.go`, `internal/webhooks/deliver.go`,
`internal/webhooks/deliver_test.go`, `internal/webhooks/migrations/000001_init.up.sql` and
`.down.sql`, `cmd/git-platform/main.go`, `proto/novaforge/git/v1/git.proto`,
`internal/edge/routes.go`, `web/src/screens/Repos.tsx`

Interfaces: consumes `events.Publish`, `events.StreamGitPush`, `events.StreamRunsDeleted`
and the CI result stream; produces
`webhooks.Hook{ID, OrgID, RepoID uuid.UUID; URL, Events []string; Active bool}`,
`(s *Store) CreateHook`, `(s *Store) ListHooks`, `(s *Store) DeleteHook`,
`(s *Store) SetSecret(ctx, id uuid.UUID, secret string) error`,
`(w *Worker) Run(ctx context.Context) error`.

- [ ] Write the failing test `internal/webhooks/deliver_test.go`:
      `func TestWebhookDelivered(t *testing.T)` stands up an `httptest` receiver, registers a
      hook, publishes a push event, and asserts the receiver got a POST whose
      `X-NovaForge-Signature` is HMAC-SHA256 of the exact body under the hook's secret, and
      that the delivery row records the response status. Then
      `func TestWebhookRetriesAreBounded(t *testing.T)` points a hook at a receiver that always
      500s and asserts the attempt count stops at the bound and the delivery is left failed.
      `expect FAIL with "no such package internal/webhooks"`.
- [ ] Write `func TestWebhookSecretNeverReadBack(t *testing.T)`: set a secret, rotate it, and
      assert no list or get response contains either value.
- [ ] Add the migration: `hooks(id, org_id, repo_id, url, events text[], secret bytea,
active)` with the secret encrypted under the service KEK like `secrets.secret_values`,
      and `hook_deliveries(id, hook_id, event, status_code, error, attempt, at)`.
- [ ] Implement the worker as a Redis Streams consumer group, so two replicas do not deliver
      the same event twice, and bound retries with a backoff that gives up.
- [ ] Wire the worker into `cmd/git-platform/main.go` — and assert in the test that the
      consumer is registered, because a worker nothing starts is this repository's most common
      defect.
- [ ] Add the RPCs, edge routes under `/api/v1/orgs/{org}/repos/{repo}/hooks`, `make openapi`,
      and a hooks panel with delivery history in `web/src/screens/Repos.tsx`.
- [ ] Commit as `feat(webhooks): notify an endpoint when something happens`.

## Task 5: Releases with assets

Files: `internal/gitops/releases.go`, `internal/gitops/releases_test.go`,
`internal/gitops/migrations/00000N_releases.up.sql` and `.down.sql`,
`internal/gitops/grpc.go`, `proto/novaforge/git/v1/git.proto`,
`internal/edge/routes.go`, `web/src/screens/Repos.tsx`

Interfaces: consumes `blobstore.Client.Put/Get/Delete`; produces
`gitops.Release{ID, RepoID uuid.UUID; Tag, Name, Body string; Assets []Asset}`,
`(s *Store) CreateRelease`, `(s *Store) ListReleases`, `(s *Store) DeleteRelease`,
`(s *Store) AddAsset(ctx, releaseID uuid.UUID, name string, r io.Reader, size int64) error`.

- [ ] Write the failing test `internal/gitops/releases_test.go`:
      `func TestReleaseWithAssets(t *testing.T)` pushes a tag, creates a release on it, uploads
      an asset, downloads it and compares bytes, asserts creating a release on a tag that does
      not exist is refused, and asserts deleting the release deletes its blobstore objects.
      `expect FAIL with "undefined: CreateRelease"`.
- [ ] Add the migration: `releases(id, org_id, repo_id, tag, name, body, created_at)` with
      `UNIQUE(repo_id, tag)`, and `release_assets(id, release_id, name, size, content_type,
blob_key)`.
- [ ] Key blobstore objects `org/<org_id>/repo/<repo_id>/release/<release_id>/<asset_id>` so an
      object cannot be reached from another organization by guessing a name.
- [ ] Add the RPCs, edge routes under `/api/v1/orgs/{org}/repos/{repo}/releases`, with the
      asset download streaming rather than buffered; `make openapi`.
- [ ] Commit as `feat(git): releases carrying downloadable assets`.

## Task 6: Git LFS

Files: `internal/gitops/lfs.go`, `internal/gitops/lfs_test.go`,
`internal/gitops/migrations/00000N_lfs.up.sql` and `.down.sql`,
`internal/gitops/http.go`, `internal/service/service.go`,
`deploy/helm/novaforge/values.yaml`, `deploy/helm/novaforge/templates/services.tpl`

Interfaces: consumes `blobstore.Client`; produces the batch endpoint
`POST /{org}/{repo}.git/info/lfs/objects/batch` and object transfer
`PUT|GET /{org}/{repo}.git/info/lfs/objects/{oid}`, plus
`(s *Store) LFSObject(ctx, repoID uuid.UUID, oid string) (int64, bool, error)`.

- [ ] Write the failing test `internal/gitops/lfs_test.go`:
      `func TestLFSRoundTrip(t *testing.T)` skips unless `git lfs version` succeeds, then tracks
      a pattern, commits a 5 MiB file, pushes, clones fresh and compares the file's bytes,
      and asserts the Git repository contains the pointer rather than the payload.
      `expect FAIL with "404 on info/lfs/objects/batch"`.
- [ ] Write `func TestLFSQuotaAndOwnership(t *testing.T)`: an object uploaded for one
      organization's repository is not retrievable with another organization's credential, and
      an upload declaring a size over `NF_LFS_MAX_OBJECT_BYTES` is refused before any bytes are
      stored.
- [ ] Add the migration: `lfs_objects(org_id, repo_id, oid, size, created_at)` with
      `PRIMARY KEY(repo_id, oid)`; the payload lives in the blobstore keyed by oid under the
      repository, never in the Git directory.
- [ ] Implement the batch API for `upload` and `download` operations only, authorizing exactly
      as the Git transport does — the same credential path, so LFS cannot be a way around
      repository access.
- [ ] Add `NF_LFS_MAX_OBJECT_BYTES` to `internal/service/service.go`, the chart values and the
      service template, and read it — a config field nothing reads is this repository's second
      most common defect.
- [ ] Commit as `feat(git): Git LFS over the existing transport`.

## Task 7: Import and mirroring

Files: `internal/gitops/mirror.go`, `internal/gitops/mirror_test.go`,
`internal/gitops/migrations/00000N_mirrors.up.sql` and `.down.sql`,
`internal/gitops/grpc.go`, `cmd/git-platform/main.go`,
`proto/novaforge/git/v1/git.proto`, `internal/edge/routes.go`, `web/src/screens/Repos.tsx`

Interfaces: produces
`(s *Store) ImportRepo(ctx context.Context, orgID uuid.UUID, name, remote string, credential string) (Repo, error)`,
`(s *Store) SetMirror(ctx, repoID uuid.UUID, remote string, interval time.Duration) error`,
`(m *Mirrorer) Run(ctx context.Context) error`.

- [ ] Write the failing test `internal/gitops/mirror_test.go`:
      `func TestImportFromRemote(t *testing.T)` serves a source repository from a temporary
      bare directory over the platform's own transport, imports it, and asserts the commits,
      branches and tags all arrived; then asserts an import from an unreachable remote fails
      and leaves no repository row and no directory. `expect FAIL with "undefined: ImportRepo"`.
- [ ] Write `func TestMirrorRefresh(t *testing.T)`: a mirrored repository picks up a new
      upstream commit when the mirrorer runs, and a push to a mirror is refused naming upstream
      as the owner of its history.
- [ ] Add the migration: `mirrors(org_id, repo_id, remote, interval_seconds, last_synced_at,
last_error)` with the remote credential encrypted under the service KEK, never in the
      remote URL in plain text and never in a log.
- [ ] Implement import as `git clone --mirror` into a throwaway directory that is moved into
      place only once complete, so a failed import leaves nothing behind.
- [ ] Wire the mirrorer into `cmd/git-platform/main.go` and assert in the test that it is
      started.
- [ ] Commit as `feat(git): import a repository and keep a mirror fresh`.

## Task 8: Forks and cross-fork Engineering Runs

Files: `internal/gitops/fork.go`, `internal/gitops/fork_test.go`,
`internal/gitops/migrations/00000N_forks.up.sql` and `.down.sql`,
`internal/reviews/store.go`, `internal/reviews/merge.go`, `internal/reviews/fork_test.go`,
`proto/novaforge/git/v1/git.proto`, `proto/novaforge/reviews/v1/reviews.proto`,
`internal/edge/routes.go`, `web/src/screens/Repos.tsx`, `web/src/screens/RunDetail.tsx`

Interfaces: produces
`(s *Store) ForkRepo(ctx context.Context, srcRepoID, toOrg uuid.UUID, name string) (Repo, error)`;
extends the Engineering Run with `source_repo_id`, so a run's source may be another
repository, and `reviews.Merger` fetches the source ref from the fork before merging.

- [ ] Write the failing test `internal/gitops/fork_test.go`:
      `func TestForkCarriesHistoryAndIsolatesWrites(t *testing.T)` forks a repository, asserts
      the fork has the parent's commits and records its parent id, pushes to the fork and
      asserts the parent's refs are unchanged. `expect FAIL with "undefined: ForkRepo"`.
- [ ] Write the failing test `internal/reviews/fork_test.go`:
      `func TestCrossForkRun(t *testing.T)` opens a run whose source is a fork and whose target
      is the parent, has an independent reviewer approve it, and asserts it merges and the
      parent's target branch carries the change — by the same gate and approval rules as a
      branch run, with no path that skips them.
- [ ] Add the migrations: `repositories.parent_repo_id uuid` nullable, and
      `runs.source_repo_id uuid` nullable defaulting to the run's own repository.
- [ ] Implement the fork as `git clone --mirror` of the parent into the new repository, then
      recording the parent id; the fork is a full repository, not a reference to the parent.
- [ ] Extend `reviews.Merger` to fetch the source ref from `source_repo_id` into the target's
      throwaway clone before merging, so the existing merge path is reused rather than a second
      one written.
- [ ] Commit as `feat(git): forks, and Engineering Runs whose source is a fork`.

## Task 9: HTTPS for the Git transport

Files: `deploy/helm/novaforge/templates/services.tpl`,
`deploy/helm/novaforge/values.yaml`, `deploy/helm/novaforge/templates/certificate.tpl`,
`cmd/git-platform/main.go`, `internal/service/service.go`, `tests/e2e/deploy_test.sh`

Interfaces: consumes `cert-manager.io/v1` `Certificate` in the cluster; produces
`NF_GIT_TLS_CERT_FILE` and `NF_GIT_TLS_KEY_FILE` read by `cmd/git-platform`.

- [ ] Write the failing e2e step in `tests/e2e/deploy_test.sh`: clone over
      `https://$GIT_IP:8443/...` against the served certificate and assert it succeeds, and
      assert a plaintext `http://` clone on the TLS port is refused rather than downgraded.
      `expect FAIL with "connection refused on 8443"`.
- [ ] Add a cert-manager `Certificate` for the git-platform service name, issued by the
      cluster issuer, and mount it read-only into the pod.
- [ ] Serve TLS in `cmd/git-platform` when both files are configured, keeping the plaintext
      listener for in-cluster callers only and refusing it on the TLS port.
- [ ] Add the values, the env wiring and `NF_GIT_TLS_*` to `internal/service/service.go`, and
      read them.
- [ ] Commit as `feat(git): serve the Git transport over HTTPS`.
