# applications — implementation plan

Status: complete
Spec: .procoder/specs/applications.md

## Goal

Group an organization's repositories into Applications with a combined overview, cross-member search, composite tagged releases, and cross-repository change sets that merge all-or-nothing, including Agent Runs confined to the application's repositories.

## Architecture

git-platform owns applications, membership, application releases and the merge-set journal (they need repositories and the `git` binary); work-reviews owns change sets and drives a merge set through git-platform RPCs; work, agents and graph each gain an application or multi-repository field, and capability grants gain the set of repositories they apply to, enforced where every ref update is already authorized (`requireGrants`). The edge composes the overview by fanning out to git-platform, CI and work-reviews, and the CLI and GUI are thin clients of the new REST routes.

## Constraints

Copied verbatim from the spec:

- Organizations are a hard boundary. Every query carries the org from `authz.FromContext`. An application and all its members are in one org, and a repository transferred to another org leaves its application.
- One schema per service, and no cross-schema reads. git-platform owns applications, membership and application releases (they need the repositories and git). work-reviews owns change sets and calls git-platform over RPC to validate membership and to merge.
- Git is the `git` binary. Writes happen in throwaway clones, and refs are published with `git update-ref --stdin`, verifying old values, as `Merge` does today.
- An application holds at most 100 repositories. The overview fans out with bounded concurrency (8). It answers within 3 s for 20 members on kw, and says "unavailable" for a member whose data did not arrive in time rather than leaving it out silently.
- Capability grants constrain agents, not human members. A human merges a change set with ordinary write access to each repository in it.
- A screen says when the deployment cannot answer, and never renders invented data.
- MCP, CLI, REST and GUI ship together for each capability.

From CLAUDE.md, binding on every task:

- The org predicate comes from `authz.FromContext` / `scopeFromContext`, never from a request field. A repository-limited (outside collaborator) scope is admitted only where the code says so, filtering by `scope.Repos`.
- No service reads another's schema. work-reviews, work and agents learn about applications only through `GitService` RPCs.
- Write the failing test first, run it, and see it fail for the stated reason before implementing; a regression test is proven by reverting the fix.
- Tests run against real PostgreSQL, Redis and MinIO: `source hack/env.sh` before `go test`, or the datastore suites `t.Skip` and look green.
- `make generate` after every proto edit; `make openapi` after every route edit; `gofmt -l internal cmd` prints nothing.
- Helm templates are `.tpl` files. kw is deployed only by `hack/build-images.sh` → `hack/promote-kw.sh <tag>` → commit and push `values-kw.yaml`; never `kubectl set image`, never `helm upgrade`.
- Commit messages carry no AI-attribution trailers.

## Task 1: Protos and migrations for applications and membership

Files:

- `proto/novaforge/git/v1/git.proto` — `Application` message, `Repo.application_id`, application RPCs, release RPCs, merge-set RPCs (declared here so `make generate` runs once for git-platform).
- `internal/gitops/migrations/000010_applications.up.sql` / `.down.sql` — `applications`, `repositories.application_id`, same-org trigger.
- `internal/gitops/migrations/000011_application_releases.up.sql` / `.down.sql` — `application_releases`, `application_release_members`.
- `internal/gitops/migrations/000012_merge_sets.up.sql` / `.down.sql` — `merge_sets`, `merge_set_items`.
- `gen/novaforge/git/v1/*` — regenerated.

Interfaces (produced, consumed by Tasks 2, 5, 6, 8, 10):

```proto
message Application {
  string id = 1;
  string org_id = 2;
  string name = 3;
  string description = 4;
  string created_at = 5;
  repeated Repo members = 6;      // only members the caller may read
  int32 hidden_members = 7;       // members the caller may not read
}
// Repo gains:
string application_id = 7;

rpc CreateApplication(CreateApplicationRequest) returns (CreateApplicationResponse);   // {name, description} -> {application}
rpc GetApplication(GetApplicationRequest) returns (GetApplicationResponse);           // {application: id or name} -> {application}
rpc ListApplications(ListApplicationsRequest) returns (ListApplicationsResponse);     // {} -> {applications}
rpc UpdateApplication(UpdateApplicationRequest) returns (UpdateApplicationResponse);  // {application, name, description} -> {application}
rpc DeleteApplication(DeleteApplicationRequest) returns (DeleteApplicationResponse);  // {application}
rpc SetRepoApplication(SetRepoApplicationRequest) returns (SetRepoApplicationResponse); // {repo, application (empty clears), move} -> {repo}
rpc CreateApplicationRelease(CreateApplicationReleaseRequest) returns (CreateApplicationReleaseResponse);
rpc ListApplicationReleases(ListApplicationReleasesRequest) returns (ListApplicationReleasesResponse);
rpc GetApplicationRelease(GetApplicationReleaseRequest) returns (GetApplicationReleaseResponse);
rpc CompareApplicationReleases(CompareApplicationReleasesRequest) returns (CompareApplicationReleasesResponse);
rpc PrepareMergeSet(PrepareMergeSetRequest) returns (PrepareMergeSetResponse);
rpc CommitMergeSet(CommitMergeSetRequest) returns (CommitMergeSetResponse);
rpc GetMergeSet(GetMergeSetRequest) returns (GetMergeSetResponse);

message ReleasePin { string repo_id = 1; string commit_sha = 2; }
message CreateApplicationReleaseRequest { string application = 1; string version = 2; string name = 3; string notes = 4; repeated ReleasePin pins = 5; }
message ApplicationReleaseMember { string repo_id = 1; string repo_name = 2; string commit_sha = 3; string tag = 4; }
message ApplicationRelease { string id = 1; string application_id = 2; string version = 3; string name = 4; string notes = 5; string created_at = 6; repeated ApplicationReleaseMember members = 7; repeated string skipped_repos = 8; }
message CreateApplicationReleaseResponse { ApplicationRelease release = 1; }
message ListApplicationReleasesRequest { string application = 1; }
message ListApplicationReleasesResponse { repeated ApplicationRelease releases = 1; }
message GetApplicationReleaseRequest { string application = 1; string version = 2; }
message GetApplicationReleaseResponse { ApplicationRelease release = 1; }
message CompareApplicationReleasesRequest { string application = 1; string from = 2; string to = 3; }
message MemberRange { string repo_id = 1; string repo_name = 2; string from_sha = 3; string to_sha = 4; repeated Commit commits = 5; }
message CompareApplicationReleasesResponse { repeated MemberRange members = 1; }

message MergeSetItem { string repo_id = 1; string source_ref = 2; string target_ref = 3; string method = 4; string expected_source_sha = 5; string expected_target_sha = 6; string message = 7; string merge_sha = 8; string state = 9; }
message MergeSet { string id = 1; string state = 2; repeated MergeSetItem items = 3; string failure = 4; }
message PrepareMergeSetRequest { repeated MergeSetItem items = 1; }
message PrepareMergeSetResponse { MergeSet set = 1; }
message CommitMergeSetRequest { string id = 1; }
message CommitMergeSetResponse { MergeSet set = 1; }
message GetMergeSetRequest { string id = 1; }
message GetMergeSetResponse { MergeSet set = 1; }
```

- [ ] Add the messages and RPCs above to `proto/novaforge/git/v1/git.proto` (request/response messages named `<Rpc>Request` / `<Rpc>Response` with the fields in the comments), then run `make generate`; expect `buf lint` clean and `go build ./...` to pass.
- [ ] Write `internal/gitops/migrations/000010_applications.up.sql`:

```sql
CREATE TABLE IF NOT EXISTS applications (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    name text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    description text NOT NULL DEFAULT '',
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name),
    UNIQUE (org_id, id)
);
ALTER TABLE repositories ADD COLUMN IF NOT EXISTS application_id uuid NULL
    REFERENCES applications (id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS repositories_application_idx ON repositories (application_id);
-- An application never holds a repository of another organization, however
-- the row came to say so: the transfer path clears it, and this refuses the rest.
CREATE OR REPLACE FUNCTION repositories_application_same_org() RETURNS trigger AS $$
BEGIN
    IF NEW.application_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM applications a WHERE a.id = NEW.application_id AND a.org_id = NEW.org_id
    ) THEN
        RAISE EXCEPTION 'application % is not in organization %', NEW.application_id, NEW.org_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS repositories_application_same_org ON repositories;
CREATE TRIGGER repositories_application_same_org BEFORE INSERT OR UPDATE OF application_id, org_id
    ON repositories FOR EACH ROW EXECUTE FUNCTION repositories_application_same_org();
```

and the `.down.sql` dropping the trigger, function, column and table in reverse order.

- [ ] Write `000011_application_releases.up.sql`:

```sql
CREATE TABLE IF NOT EXISTS application_releases (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    application_id uuid NOT NULL REFERENCES applications (id) ON DELETE CASCADE,
    version text NOT NULL CHECK (version ~ '^[0-9A-Za-z][0-9A-Za-z._-]{0,62}$'),
    name text NOT NULL DEFAULT '',
    notes text NOT NULL DEFAULT '',
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (application_id, version)
);
CREATE TABLE IF NOT EXISTS application_release_members (
    release_id uuid NOT NULL REFERENCES application_releases (id) ON DELETE CASCADE,
    repo_id uuid NULL REFERENCES repositories (id) ON DELETE SET NULL,
    repo_name text NOT NULL,
    commit_sha text NOT NULL,
    tag text NOT NULL,
    PRIMARY KEY (release_id, repo_name)
);
```

with its `.down.sql`.

- [ ] Write `000012_merge_sets.up.sql`:

```sql
CREATE TABLE IF NOT EXISTS merge_sets (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    state text NOT NULL CHECK (state IN ('prepared', 'committing', 'committed', 'rolling_back', 'rolled_back', 'failed')),
    failure text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS merge_set_items (
    set_id uuid NOT NULL REFERENCES merge_sets (id) ON DELETE CASCADE,
    seq int NOT NULL,
    repo_id uuid NOT NULL,
    source_ref text NOT NULL,
    target_ref text NOT NULL,
    method text NOT NULL DEFAULT 'merge',
    expected_target_sha text NOT NULL,
    merge_sha text NOT NULL,
    state text NOT NULL CHECK (state IN ('pending', 'published', 'restored', 'reverted', 'conflict')),
    PRIMARY KEY (set_id, repo_id)
);
CREATE INDEX IF NOT EXISTS merge_sets_open_idx ON merge_sets (state) WHERE state IN ('committing', 'rolling_back');
```

with its `.down.sql`.

- [ ] Run `source hack/env.sh && go test ./internal/gitops/ -run TestCreateRepoInitialisesBareRepo -count=1`; expect PASS (the migrations apply on a real database and nothing existing broke).
- [ ] Commit: `feat(gitops): schema and API for applications, application releases and merge sets`.

## Task 2: git-platform application CRUD and membership

Files:

- `internal/gitops/applications.go` — `applicationRow`, scans, membership rules.
- `internal/gitops/grpc_applications.go` — the six application RPCs on `*Server`.
- `internal/gitops/grpc.go` — `repoRow.ApplicationID`, selected by `repoByName` and `ListRepos`, rendered by `toProtoRepo`.
- `internal/gitops/admin.go` — `TransferRepo` clears `application_id` in its transaction.
- `internal/gitops/applications_test.go` — the two S-1 tests.

Interfaces (produced): `func (s *Server) CreateApplication/GetApplication/ListApplications/UpdateApplication/DeleteApplication/SetRepoApplication(ctx, req)` with the request types of Task 1; `func (s *Server) applicationByRef(ctx context.Context, orgID uuid.UUID, ref string) (applicationRow, error)` (name or id, `codes.NotFound` otherwise); `const maxApplicationMembers = 100`. Consumed by Tasks 5, 6, 8, 9, 10.

- [ ] Write the failing tests in `internal/gitops/applications_test.go`:

```go
package gitops_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestApplicationMembershipIsExclusiveAndOrgScoped(t *testing.T) {
	srv, _ := newGitGRPCServer(t)
	org, other := uuid.New(), uuid.New()
	ctx, otherCtx := scopedCtx(org), scopedCtx(other)
	shop, err := srv.CreateApplication(ctx, &gitv1.CreateApplicationRequest{Name: "shop", Description: "storefront"})
	if err != nil {
		t.Fatal(err)
	}
	billing, err := srv.CreateApplication(ctx, &gitv1.CreateApplicationRequest{Name: "billing"})
	if err != nil {
		t.Fatal(err)
	}
	api, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.SetRepoApplication(ctx, &gitv1.SetRepoApplicationRequest{Repo: "api", Application: "shop"}); err != nil {
		t.Fatalf("add api to shop: %v", err)
	}
	_, err = srv.SetRepoApplication(ctx, &gitv1.SetRepoApplicationRequest{Repo: "api", Application: "billing"})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "shop") {
		t.Fatalf("adding a member of shop to billing: %v, want FailedPrecondition naming shop", err)
	}
	moved, err := srv.SetRepoApplication(ctx, &gitv1.SetRepoApplicationRequest{Repo: "api", Application: "billing", Move: true})
	if err != nil || moved.GetRepo().GetApplicationId() != billing.GetApplication().GetId() {
		t.Fatalf("move api to billing: %+v %v", moved, err)
	}
	if _, err := srv.UpdateApplication(ctx, &gitv1.UpdateApplicationRequest{Application: "shop", Name: "storefront"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	// Another organization names the application by id and the repository by id:
	// both are resolved in the caller's own organization and found nowhere.
	if _, err := srv.CreateRepo(otherCtx, &gitv1.CreateRepoRequest{Name: "intruder"}); err != nil {
		t.Fatal(err)
	}
	_, err = srv.SetRepoApplication(otherCtx, &gitv1.SetRepoApplicationRequest{Repo: "intruder", Application: shop.GetApplication().GetId()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("other org added a repository to shop: %v, want NotFound", err)
	}
	_, err = srv.SetRepoApplication(otherCtx, &gitv1.SetRepoApplicationRequest{Repo: api.GetRepo().GetId(), Application: ""})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("other org detached api: %v, want NotFound", err)
	}
	if _, err := srv.DeleteApplication(ctx, &gitv1.DeleteApplicationRequest{Application: "storefront"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := srv.GetApplication(ctx, &gitv1.GetApplicationRequest{Application: "storefront"}); status.Code(err) != codes.NotFound {
		t.Fatalf("deleted application still found: %v", err)
	}
}

func TestMemberLeavesApplicationOnDeleteAndTransfer(t *testing.T) {
	srv, _ := newGitGRPCServer(t)
	org, dest := uuid.New(), uuid.New()
	ctx := scopedCtx(org)
	if _, err := srv.CreateApplication(ctx, &gitv1.CreateApplicationRequest{Name: "shop"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api", "web", "worker"} {
		if _, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: name}); err != nil {
			t.Fatal(err)
		}
		if _, err := srv.SetRepoApplication(ctx, &gitv1.SetRepoApplicationRequest{Repo: name, Application: "shop"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := srv.DeleteRepo(ownerCtx(org), &gitv1.DeleteRepoRequest{Repo: "worker"}); err != nil {
		t.Fatalf("delete worker: %v", err)
	}
	moved, err := srv.TransferRepo(ownerCtx(org), &gitv1.TransferRepoRequest{Repo: "web", ToOrg: dest.String()})
	if err != nil {
		t.Fatalf("transfer web: %v", err)
	}
	if moved.GetRepo().GetApplicationId() != "" {
		t.Fatalf("transferred repository still names application %q", moved.GetRepo().GetApplicationId())
	}
	app, err := srv.GetApplication(ctx, &gitv1.GetApplicationRequest{Application: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if len(app.GetApplication().GetMembers()) != 1 || app.GetApplication().GetMembers()[0].GetName() != "api" {
		t.Fatalf("members after delete and transfer = %v, want only api", app.GetApplication().GetMembers())
	}
	if _, err := srv.DeleteApplication(ctx, &gitv1.DeleteApplicationRequest{Application: "shop"}); err != nil {
		t.Fatal(err)
	}
	api, err := srv.GetRepo(ctx, &gitv1.GetRepoRequest{Repo: "api"})
	if err != nil || api.GetRepo().GetApplicationId() != "" {
		t.Fatalf("api after its application was deleted: %+v %v", api, err)
	}
}
```

`ownerCtx(org)` is `scopedCtx` with `Role: "owner"`, already used by `delete_test.go`; if it is not exported to the package's tests under that name, add it to `applications_test.go` as `authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorID: uuid.New(), ActorKind: "user", Role: "owner"})`.

- [ ] Run `source hack/env.sh && go test ./internal/gitops/ -run 'TestApplicationMembershipIsExclusiveAndOrgScoped|TestMemberLeavesApplicationOnDeleteAndTransfer' -count=1`; expect FAIL with `srv.CreateApplication undefined` (compile error: `*gitops.Server` has no method `CreateApplication` until the RPCs exist; after `make generate` the embedded `UnimplementedGitServiceServer` makes it compile and FAIL with "unknown method CreateApplication" / codes.Unimplemented).
- [ ] Implement `internal/gitops/applications.go` and `grpc_applications.go`: every query `WHERE org_id = $scope.OrgID`; `SetRepoApplication` runs in one transaction with `SELECT ... FOR UPDATE` on the repository row, refuses `FailedPrecondition "repository %q is in application %q"` when it is in another application and `move` is false, refuses `FailedPrecondition "application %q already holds 100 repositories"` at `maxApplicationMembers`; `GetApplication` lists members and, for `scope.RepoLimited`, returns only members whose id is in `scope.Repos` and counts the rest in `hidden_members`; `ListApplications` for a repo-limited scope returns only applications with at least one readable member; Create/Update/Delete/SetRepoApplication refuse a repo-limited scope with `PermissionDenied`.
- [ ] Add `ApplicationID` to `repoRow`, select `application_id` in `repoByName` and `ListRepos` (scan through `*uuid.UUID`), set `Repo.ApplicationId` in `toProtoRepo`.
- [ ] In `TransferRepo`, change the UPDATE to `UPDATE gitplatform.repositories SET org_id = $1, application_id = NULL WHERE id = $2 AND org_id = $3` and return `ApplicationId: ""`.
- [ ] Run the two tests again; expect PASS. Revert the `application_id = NULL` in `TransferRepo`, rerun, expect FAIL with `transferred repository still names application` (or the trigger's `check_violation`), restore, expect PASS.
- [ ] Commit: `feat(gitops): applications and exclusive, org-scoped membership`.

## Task 3: Repository-scoped capability grants

Files:

- `internal/capability/migrations/000004_grant_repos.up.sql` / `.down.sql` — `repo_ids uuid[] NULL`.
- `internal/capability/grant.go` — `Grant.RepoIDs`, `CanWriteRepo`, `Issue`/`Resolve`/`ListActive`/`ResolveScoped` read and write `repo_ids`.
- `internal/capability/issuance.go` — the intent insert writes `repo_ids`.
- `internal/capability/proto.go`, `proto/novaforge/identity/v1/identity.proto` — `Grant.repo_ids = 11`.
- `internal/gitops/credentials.go` — `requireGrants` checks the repository; `NewGrantCapFunc` resolves the pushed repository's id.
- `internal/gitops/grpc.go` — `authorizeAgentWrite` passes the repository id.
- `cmd/git-platform/main.go`, `internal/platformtest/platform.go` — `NewGrantCapFunc(grants, repoIDs)`.
- `internal/agents/grpc.go` — StartRun sets `RepoIDs: []uuid.UUID{repoID}`.
- `internal/capability/grant_test.go`, `internal/agents/grpc_test.go` — the two S-9 tests.

Interfaces (produced):

```go
// Grant gains:
RepoIDs []uuid.UUID // nil: every repository in OrgID (grants issued before this change only)
func CanWriteRepo(g Grant, repoID uuid.UUID) error
// gitops:
type RepoIDResolver func(ctx context.Context, orgID uuid.UUID, repo string) (uuid.UUID, error)
func NewGrantCapFunc(grants GrantLister, repoIDs RepoIDResolver) CapFunc
func requireGrants(ctx context.Context, grants GrantLister, s authz.Scope, orgID, repoID uuid.UUID, refs []string) error
func (s *Server) RepoIDResolver() RepoIDResolver
```

Consumed by Task 9 (application Agent Runs issue `RepoIDs` = the members).

- [ ] Write the failing tests. In `internal/capability/grant_test.go`:

```go
func TestGrantRefusesRepositoryOutsideItsSet(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	g := capability.Grant{WriteBranch: "agents/NF-7/", RepoIDs: []uuid.UUID{a}}
	if err := capability.CanWriteRepo(g, a); err != nil {
		t.Fatalf("write to the named repository refused: %v", err)
	}
	err := capability.CanWriteRepo(g, b)
	if err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("write to a repository outside the grant: %v, want not permitted", err)
	}
	if err := capability.CanWriteRef(g, "refs/heads/agents/NF-7/x"); err != nil {
		t.Fatalf("branch check changed: %v", err)
	}
	// A grant issued before repo_ids existed keeps its meaning.
	if err := capability.CanWriteRepo(capability.Grant{WriteBranch: "agents/NF-7/"}, b); err != nil {
		t.Fatalf("legacy grant refused: %v", err)
	}
	// And the set survives the database.
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := database.Migrate(url, "gitplatform", os.DirFS("migrations")); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	g.OrgID, g.SubjectID, g.SubjectKind, g.ExpiresAt = uuid.New(), uuid.New(), "agent", time.Now().Add(time.Hour)
	issued, err := capability.NewStore(pool).Issue(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	active, err := capability.NewStore(pool).ListActive(context.Background(), g.OrgID, g.SubjectID)
	if err != nil || len(active) != 1 || len(active[0].RepoIDs) != 1 || active[0].RepoIDs[0] != a || active[0].ID != issued.ID {
		t.Fatalf("stored grant = %+v %v, want repo_ids [%s]", active, err, a)
	}
}
```

In `internal/agents/grpc_test.go`:

```go
func TestSingleRepoRunGrantNamesItsRepo(t *testing.T) {
	orgID, repoID := uuid.New(), uuid.New()
	work := &stubWorkClient{item: &workv1.WorkItem{Id: uuid.New().String(), Key: "NF-2", RepoId: repoID.String(), State: "open"}}
	srv, store := newGRPCServer(t, work)
	ctx := scopedCtx(orgID)
	agent := mustCreateAgent(t, store, ctx, orgID)
	resp, err := srv.StartRun(ctx, &agentsv1.StartRunRequest{AgentId: agent.ID.String(), RepoId: repoID.String(), WorkItemKey: "NF-2", SponsorId: actorOf(ctx)})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	grantID := uuid.MustParse(resp.GetRun().GetGrantId())
	grant, err := capability.NewStore(grantsPool(t)).Resolve(scopedCtx(orgID), grantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grant.RepoIDs) != 1 || grant.RepoIDs[0] != repoID {
		t.Fatalf("grant repo_ids = %v, want exactly [%s]: a branch prefix alone reaches every repository in the org", grant.RepoIDs, repoID)
	}
}
```

- [ ] Run `source hack/env.sh && go test ./internal/capability/ ./internal/agents/ -run 'TestGrantRefusesRepositoryOutsideItsSet|TestSingleRepoRunGrantNamesItsRepo' -count=1`; expect FAIL with `unknown field RepoIDs in struct literal of type capability.Grant`.
- [ ] Add `000004_grant_repos.up.sql`: `ALTER TABLE capability_grants ADD COLUMN IF NOT EXISTS repo_ids uuid[] NULL;` and the matching `.down.sql`. Add `repo_ids` to every INSERT and SELECT in `grant.go` and `issuance.go` (scan into `[]uuid.UUID`; pgx maps `uuid[]`), to `identity.proto` `Grant` (`repeated string repo_ids = 11;`), and to `GrantProto`/`GrantFromProto`; `make generate`.
- [ ] Implement `CanWriteRepo`: `nil` RepoIDs permits; otherwise `fmt.Errorf("write to repository %s not permitted; grant covers %d repositories", repoID, len(g.RepoIDs))` unless listed.
- [ ] Change `requireGrants` to take `repoID` and require, for each ref, a grant passing both `CanWriteRef(g, ref)` and `CanWriteRepo(g, repoID)`. `authorizeAgentWrite(ctx, scope, repoID, refs...)` passes `row.ID` from `Merge` and from every other caller (`grep -n authorizeAgentWrite internal/gitops`). `NewGrantCapFunc(grants, repoIDs)` resolves `repoIDs(ctx, orgID, repo)` before `requireGrants`; `(*Server).RepoIDResolver()` returns a closure over `repoByName`. Update `cmd/git-platform/main.go` (`gitops.NewGrantCapFunc(grants, srv.RepoIDResolver())`) and `internal/platformtest/platform.go`.
- [ ] In `internal/agents/grpc.go` StartRun, set `RepoIDs: []uuid.UUID{repoID}` on the grant.
- [ ] Run the two tests; expect PASS. Run `go test ./internal/gitops/ ./internal/capability/ ./internal/agents/ ./internal/tools/ -count=1`; expect PASS (existing transport and tool grant tests unchanged). Remove the `CanWriteRepo` call from `requireGrants`, rerun `go test ./internal/gitops/ -run TestAgentPushOutsideGrantRepos -count=1` (added in Task 9) — until then, prove it by reverting `RepoIDs` in StartRun and watching `TestSingleRepoRunGrantNamesItsRepo` fail; restore.
- [ ] Commit: `feat(capability): grants name the repositories they apply to`.

## Task 4: Search across several repositories

Files:

- `proto/novaforge/graph/v1/graph.proto` — `SearchCodeRequest.repo_ids = 4`, `SearchKnowledgeRequest.repo_ids = 4`, `CodeChunk.repo_id = 6`, `KnowledgeEntry.repo_id = 8`.
- `internal/graph/grpc.go` — `SearchCode`/`SearchKnowledge` accept either `repo_id` or `repo_ids` (at most 100), rank together, label each hit.
- `internal/graph/semantic.go` (or the file defining `VectorStore.Search`) — `SearchRepos(ctx, orgID, repoIDs, vec, k)`.
- `internal/graph/application_search_test.go` — the S-3 unit test.

Interfaces (produced): `func (v *VectorStore) SearchRepos(ctx context.Context, orgID uuid.UUID, repoIDs []uuid.UUID, vec []float32, k int) ([]Chunk, error)`; `Chunk.RepoID uuid.UUID`. Consumed by Task 10 (`GET /apps/{app}/search`).

- [ ] Write the failing test `internal/graph/application_search_test.go`:

```go
package graph_test

import (
	"testing"

	"github.com/google/uuid"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"github.com/novaforge/novaforge/internal/graph"
)

func TestApplicationSearchSpansOnlyMembers(t *testing.T) {
	srv := newGRPCServer(t)
	org := uuid.New()
	api, web, outside := uuid.New(), uuid.New(), uuid.New()
	ctx := scopedCtx(org, uuid.New())
	vs := graph.NewVectorStore(srv.Store.Pool())
	emb := make([]float32, graph.EmbeddingDim)
	seed := func(repo uuid.UUID, path, text string) {
		t.Helper()
		if err := vs.Upsert(ctx, org, repo, path, []graph.Chunk{{ID: uuid.New(), Path: path, StartLine: 1, EndLine: 2, Text: text, Embedding: emb}}); err != nil {
			t.Fatal(err)
		}
	}
	seed(api, "orders.go", "func PlaceOrder records a gizmo order")
	seed(web, "cart.ts", "function showGizmoCart() {}")
	seed(outside, "secret.go", "func GizmoSecret() {}")
	resp, err := srv.SearchCode(ctx, &graphv1.SearchCodeRequest{RepoIds: []string{api.String(), web.String()}, Query: "gizmo", K: 10})
	if err != nil {
		t.Fatalf("SearchCode: %v", err)
	}
	got := map[string]string{}
	for _, c := range resp.GetChunks() {
		got[c.GetPath()] = c.GetRepoId()
	}
	if got["orders.go"] != api.String() || got["cart.ts"] != web.String() {
		t.Fatalf("hits = %v, want orders.go from api and cart.ts from web, each labelled", got)
	}
	if _, leaked := got["secret.go"]; leaked {
		t.Fatal("a repository outside the application was searched")
	}
	if _, err := srv.SearchCode(ctx, &graphv1.SearchCodeRequest{RepoIds: make([]string, 101), Query: "gizmo"}); err == nil {
		t.Fatal("a search over 101 repositories was accepted")
	}
}
```

- [ ] Run `source hack/env.sh && go test ./internal/graph/ -run TestApplicationSearchSpansOnlyMembers -count=1`; expect FAIL with `unknown field RepoIds in struct literal of type graphv1.SearchCodeRequest`.
- [ ] Add the proto fields, `make generate`. In `SearchCode`: build `repoIDs` from `repo_ids` when set (refuse more than 100 with `InvalidArgument "at most 100 repositories"`; parse each with `parseRepoID`), else `[repo_id]`; semantic path calls `Vectors.SearchRepos`, lexical path uses `repo_id = ANY($2)` and selects `repo_id`; set `CodeChunk.RepoId`. Same for `SearchKnowledge` over `repo_id = ANY($2)`. `org_id = $1` stays the first predicate of every query.
- [ ] Run the test; expect PASS. Change the lexical WHERE to drop `repo_id = ANY($2)`, rerun, expect FAIL with "a repository outside the application was searched"; restore.
- [ ] Commit: `feat(graph): search code and knowledge across several repositories`.

## Task 5: Application releases, tags and compare

Files:

- `internal/gitops/app_releases.go` — `CreateApplicationRelease`, `ListApplicationReleases`, `GetApplicationRelease`, `CompareApplicationReleases` on `*Server`; `tagMember`, `untagMember`.
- `internal/gitops/app_releases_test.go` — the two S-4 tests.

Interfaces (produced): the four RPCs of Task 1; tag name `releaseTag(app, version) = app + "/" + version`. Consumed by Tasks 10–12.

- [ ] Write the failing tests `internal/gitops/app_releases_test.go`:

```go
package gitops_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/gitops"
)

func appWithMembers(t *testing.T, full *gitops.Server, ctx context.Context, names ...string) map[string]string {
	t.Helper()
	if _, err := full.CreateApplication(ctx, &gitv1.CreateApplicationRequest{Name: "shop"}); err != nil {
		t.Fatal(err)
	}
	heads := map[string]string{}
	for _, n := range names {
		if _, err := full.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: n}); err != nil {
			t.Fatal(err)
		}
		c, err := full.CreateCommit(ctx, &gitv1.CreateCommitRequest{Repo: n, Branch: "main", Message: "init " + n, Files: []*gitv1.FileChange{{Path: "README", Content: []byte(n)}}})
		if err != nil {
			t.Fatal(err)
		}
		heads[n] = c.GetSha()
		if _, err := full.SetRepoApplication(ctx, &gitv1.SetRepoApplicationRequest{Repo: n, Application: "shop"}); err != nil {
			t.Fatal(err)
		}
	}
	return heads
}

func TestApplicationReleaseTagsEveryMemberOrNone(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	heads := appWithMembers(t, srv, ctx, "api", "web", "worker")
	rel, err := srv.CreateApplicationRelease(ctx, &gitv1.CreateApplicationReleaseRequest{Application: "shop", Version: "1.0.0"})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if len(rel.GetRelease().GetMembers()) != 3 {
		t.Fatalf("release members = %v, want 3", rel.GetRelease().GetMembers())
	}
	for name, sha := range heads {
		bare := filepath.Join(root, org.String(), name+".git")
		out := runGit(t, "", "ls-remote", "--tags", bare, "refs/tags/shop/1.0.0^{}")
		if !strings.HasPrefix(out, sha) {
			t.Fatalf("%s: tag shop/1.0.0 peels to %q, want %s", name, out, sha)
		}
	}
	// A conflicting tag in one member refuses the whole release before any tag is written.
	other := runGit(t, "", "--git-dir="+filepath.Join(root, org.String(), "web.git"), "commit-tree", "-m", "elsewhere", heads["web"]+"^{tree}")
	runGit(t, "", "--git-dir="+filepath.Join(root, org.String(), "web.git"), "tag", "shop/2.0.0", strings.TrimSpace(other))
	if _, err := srv.CreateApplicationRelease(ctx, &gitv1.CreateApplicationReleaseRequest{Application: "shop", Version: "2.0.0"}); err == nil || !strings.Contains(err.Error(), "web") {
		t.Fatalf("release over a conflicting tag: %v, want a refusal naming web", err)
	}
	for _, name := range []string{"api", "worker"} {
		if out := runGit(t, "", "ls-remote", "--tags", filepath.Join(root, org.String(), name+".git"), "refs/tags/shop/2.0.0"); out != "" {
			t.Fatalf("%s gained tag shop/2.0.0 from a refused release: %s", name, out)
		}
	}
}

func TestCompareApplicationReleases(t *testing.T) {
	srv, _ := newGitGRPCServer(t)
	ctx := scopedCtx(uuid.New())
	appWithMembers(t, srv, ctx, "api", "web")
	if _, err := srv.CreateApplicationRelease(ctx, &gitv1.CreateApplicationReleaseRequest{Application: "shop", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	var want []string
	for i, msg := range []string{"fix rounding", "add refunds"} {
		c, err := srv.CreateCommit(ctx, &gitv1.CreateCommitRequest{Repo: "api", Branch: "main", Message: msg, Files: []*gitv1.FileChange{{Path: "f", Content: []byte{byte(i)}}}})
		if err != nil {
			t.Fatal(err)
		}
		want = append([]string{c.GetSha()}, want...)
	}
	if _, err := srv.CreateApplicationRelease(ctx, &gitv1.CreateApplicationReleaseRequest{Application: "shop", Version: "1.1.0"}); err != nil {
		t.Fatal(err)
	}
	// A commit after 1.1.0 must not appear: the range is between recorded SHAs, not heads.
	if _, err := srv.CreateCommit(ctx, &gitv1.CreateCommitRequest{Repo: "api", Branch: "main", Message: "later", Files: []*gitv1.FileChange{{Path: "f", Content: []byte("z")}}}); err != nil {
		t.Fatal(err)
	}
	cmp, err := srv.CompareApplicationReleases(ctx, &gitv1.CompareApplicationReleasesRequest{Application: "shop", From: "1.0.0", To: "1.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range cmp.GetMembers() {
		var got []string
		for _, c := range m.GetCommits() {
			got = append(got, c.GetSha())
		}
		switch m.GetRepoName() {
		case "api":
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("api commits = %v, want %v", got, want)
			}
		case "web":
			if len(got) != 0 {
				t.Fatalf("web commits = %v, want none", got)
			}
		}
	}
}
```

- [ ] Run `source hack/env.sh && go test ./internal/gitops/ -run 'TestApplicationReleaseTagsEveryMemberOrNone|TestCompareApplicationReleases' -count=1`; expect FAIL with `rpc error: code = Unimplemented desc = method CreateApplicationRelease not implemented`.
- [ ] Implement `CreateApplicationRelease`: resolve the application and members in scope; refuse a repo-limited scope; resolve each member's pin (request pin, else default-branch head via `git rev-parse`); skip members with no commits into `skipped_repos`; phase 1, for every member, `git rev-parse --verify -q refs/tags/<tag>^{}` in the bare repo — a tag peeling to the pin is reused, a tag elsewhere refuses `FailedPrecondition "tag %s already exists in %s at %s"`; phase 2, per member, in a throwaway `git clone --no-hardlinks --bare` create `git tag -a <tag> <sha> -m "<app> <version>"`, `git fetch` the tag object into the bare repository, publish with `git update-ref --stdin` (`start\ncreate refs/tags/<tag> <tagobj>\nprepare\ncommit\n`); on any failure, delete the tags created so far with `delete refs/tags/<tag> <tagobj>` and return the error; phase 3, insert `application_releases` and `application_release_members` (with `repo_name` snapshot) in one transaction; if that fails, delete the tags as in phase 2.
- [ ] Implement `ListApplicationReleases`, `GetApplicationRelease` (members from the table, never re-resolved from heads) and `CompareApplicationReleases` (per member present in either release: `git log --format=%H%x00%an%x00%aI%x00%s from..to` in the member's bare repository; a member in only one release has an empty `from_sha` or `to_sha` and no commits; a deleted member is reported by its `repo_name` with no commits).
- [ ] Run the tests; expect PASS. Move phase 1 after phase 2 (write tags before checking), rerun, expect FAIL with "api gained tag shop/2.0.0 from a refused release"; restore.
- [ ] Commit: `feat(gitops): composite application releases tagged in every member`.

## Task 6: Merge sets — prepare, all-or-nothing commit, restore, revert, recovery

Files:

- `internal/gitops/grpc.go` — split `mergeRefs` into `buildMerge` and `publishMerge` without changing `Merge`'s behaviour.
- `internal/gitops/merge_sets.go` — `PrepareMergeSet`, `CommitMergeSet`, `GetMergeSet`, `RecoverMergeSets`, `restoreItem`, `revertItem`.
- `cmd/git-platform/main.go` — call `srv.RecoverMergeSets(ctx)` before serving.
- `internal/gitops/merge_sets_test.go` — three S-7 git-side tests.

Interfaces (produced):

```go
func buildMerge(repoPath, sourceRef, targetRef, method, message, expectedSource, expectedTarget, sourceRepoPath string) (oldSHA, newSHA, sourceSHA string, err error)
func publishMerge(repoPath, sourceBranch, sourceSHA, targetBranch, newSHA, oldSHA string, verifySource bool) error
func (s *Server) PrepareMergeSet(ctx, *gitv1.PrepareMergeSetRequest) (*gitv1.PrepareMergeSetResponse, error)
func (s *Server) CommitMergeSet(ctx, *gitv1.CommitMergeSetRequest) (*gitv1.CommitMergeSetResponse, error)
func (s *Server) GetMergeSet(ctx, *gitv1.GetMergeSetRequest) (*gitv1.GetMergeSetResponse, error)
func (s *Server) RecoverMergeSets(ctx context.Context) error
```

Set states: `prepared → committing → committed`, or `committing → rolling_back → rolled_back`; `failed` only when a restore and a revert both fail (the set then names the repository needing a person). Item states: `pending`, `published`, `restored`, `reverted`, `conflict`. Consumed by Task 8.

- [ ] Write the failing tests `internal/gitops/merge_sets_test.go`:

```go
package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gitops"
)

type setRepo struct{ id, base, head string }

func twoBranchedRepos(t *testing.T, srv *gitops.Server, ctx context.Context) map[string]setRepo {
	t.Helper()
	out := map[string]setRepo{}
	for _, n := range []string{"api", "web"} {
		r, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: n})
		if err != nil {
			t.Fatal(err)
		}
		base, err := srv.CreateCommit(ctx, &gitv1.CreateCommitRequest{Repo: n, Branch: "main", Message: "base", Files: []*gitv1.FileChange{{Path: "a", Content: []byte("a")}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := srv.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: n, Name: "feature", FromRef: "main"}); err != nil {
			t.Fatal(err)
		}
		head, err := srv.CreateCommit(ctx, &gitv1.CreateCommitRequest{Repo: n, Branch: "feature", Message: "change", Files: []*gitv1.FileChange{{Path: "b", Content: []byte("b")}}})
		if err != nil {
			t.Fatal(err)
		}
		out[n] = setRepo{r.GetRepo().GetId(), base.GetSha(), head.GetSha()}
	}
	return out
}

func setItems(repos map[string]setRepo) []*gitv1.MergeSetItem {
	var items []*gitv1.MergeSetItem
	for _, n := range []string{"api", "web"} {
		r := repos[n]
		items = append(items, &gitv1.MergeSetItem{RepoId: r.id, SourceRef: "feature", TargetRef: "main", Method: "merge", ExpectedSourceSha: r.head, ExpectedTargetSha: r.base})
	}
	return items
}

func mainOf(t *testing.T, root string, org uuid.UUID, name string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, "", "--git-dir="+filepath.Join(root, org.String(), name+".git"), "rev-parse", "refs/heads/main"))
}

func TestMergeSetPublishesEveryTarget(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	repos := twoBranchedRepos(t, srv, ctx)
	prep, err := srv.PrepareMergeSet(ctx, &gitv1.PrepareMergeSetRequest{Items: setItems(repos)})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if mainOf(t, root, org, "api") != repos["api"].base {
		t.Fatal("prepare published a target")
	}
	done, err := srv.CommitMergeSet(ctx, &gitv1.CommitMergeSetRequest{Id: prep.GetSet().GetId()})
	if err != nil || done.GetSet().GetState() != "committed" {
		t.Fatalf("commit: %+v %v", done, err)
	}
	for _, it := range done.GetSet().GetItems() {
		name := map[string]string{repos["api"].id: "api", repos["web"].id: "web"}[it.GetRepoId()]
		if got := mainOf(t, root, org, name); got != it.GetMergeSha() || got == repos[name].base {
			t.Fatalf("%s main = %s, want merge commit %s", name, got, it.GetMergeSha())
		}
	}
}

func TestMergeSetRestoresOnMovedTarget(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	repos := twoBranchedRepos(t, srv, ctx)
	prep, err := srv.PrepareMergeSet(ctx, &gitv1.PrepareMergeSetRequest{Items: setItems(repos)})
	if err != nil {
		t.Fatal(err)
	}
	// Someone pushes to web's main after prepare.
	moved, err := srv.CreateCommit(ctx, &gitv1.CreateCommitRequest{Repo: "web", Branch: "main", Message: "racing push", Files: []*gitv1.FileChange{{Path: "c", Content: []byte("c")}}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := srv.CommitMergeSet(ctx, &gitv1.CommitMergeSetRequest{Id: prep.GetSet().GetId()})
	if err != nil {
		t.Fatalf("commit returned an error instead of an outcome: %v", err)
	}
	if res.GetSet().GetState() != "rolled_back" || !strings.Contains(res.GetSet().GetFailure(), "web") {
		t.Fatalf("set = %+v, want rolled_back naming web", res.GetSet())
	}
	if got := mainOf(t, root, org, "api"); got != repos["api"].base {
		t.Fatalf("api main = %s, want restored to %s", got, repos["api"].base)
	}
	if got := mainOf(t, root, org, "web"); got != moved.GetSha() {
		t.Fatalf("web main = %s, want the racing push %s untouched", got, moved.GetSha())
	}
}

func TestMergeSetResumesAfterCrash(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	repos := twoBranchedRepos(t, srv, ctx)
	prep, err := srv.PrepareMergeSet(ctx, &gitv1.PrepareMergeSetRequest{Items: setItems(repos)})
	if err != nil {
		t.Fatal(err)
	}
	setID := prep.GetSet().GetId()
	// Simulate a crash after publishing api only: publish it the way CommitMergeSet
	// does, and leave the journal in "committing" with api "published".
	var apiMerge string
	for _, it := range prep.GetSet().GetItems() {
		if it.GetRepoId() == repos["api"].id {
			apiMerge = it.GetMergeSha()
		}
	}
	runGitWithStdin(t, filepath.Join(root, org.String(), "api.git"),
		"start\nupdate refs/heads/main "+apiMerge+" "+repos["api"].base+"\nprepare\ncommit\n", "update-ref", "--stdin")
	pool, err := database.Connect(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(context.Background(), `UPDATE gitplatform.merge_sets SET state='committing' WHERE id=$1`, setID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE gitplatform.merge_set_items SET state='published' WHERE set_id=$1 AND repo_id=$2`, setID, repos["api"].id); err != nil {
		t.Fatal(err)
	}
	fresh := gitops.NewGRPCServer(pool, root)
	if err := fresh.RecoverMergeSets(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	got, err := fresh.GetMergeSet(ctx, &gitv1.GetMergeSetRequest{Id: setID})
	if err != nil {
		t.Fatal(err)
	}
	api, web := mainOf(t, root, org, "api"), mainOf(t, root, org, "web")
	switch got.GetSet().GetState() {
	case "rolled_back":
		if api != repos["api"].base || web != repos["web"].base {
			t.Fatalf("rolled back but api=%s web=%s", api, web)
		}
	case "committed":
		if api == repos["api"].base || web == repos["web"].base {
			t.Fatalf("committed but api=%s web=%s", api, web)
		}
	default:
		t.Fatalf("after recovery the set is %q, want rolled_back or committed", got.GetSet().GetState())
	}
}
```

- [ ] Run `source hack/env.sh && go test ./internal/gitops/ -run 'TestMergeSet' -count=1`; expect FAIL with `rpc error: code = Unimplemented desc = method PrepareMergeSet not implemented`.
- [ ] Refactor `mergeRefs` into `buildMerge` (clone, checkout, verify expected SHAs, merge, `fetch` the new commit into the bare repository; returns `oldSHA, newSHA, sourceSHA`) and `publishMerge` (the existing `update-ref --stdin` transaction); `mergeRefs` becomes `buildMerge` then `publishMerge`. Run `go test ./internal/gitops/ -run 'TestMerge' -count=1`; expect PASS (`TestMergeExpectedSourceAndNativeRefLocks` unchanged).
- [ ] Implement `PrepareMergeSet`: refuse more than 100 items and duplicate repositories (`InvalidArgument`); resolve each repository in scope (`repoByName` by id), refuse archived (`FailedPrecondition`, `ErrArchived`), `authorizeAgentWrite(ctx, scope, row.ID, target)`; `buildMerge` each; keep each candidate reachable with `update-ref refs/novaforge/mergesets/<set>/<repo> <merge_sha>`; insert `merge_sets` (`prepared`) and `merge_set_items` (`pending`, `seq` in request order) in one transaction. Any build failure deletes the candidate refs already written and returns the error naming the repository.
- [ ] Implement `CommitMergeSet`: set `committing`; for each item in `seq` order, `publishMerge(..., newSHA=merge_sha, oldSHA=expected_target_sha, verifySource=true)` and mark it `published` before the next; on the first failure mark it `conflict`, set `rolling_back` with `failure = "<repo name>: <git output>"`, and for each `published` item in reverse order call `restoreItem` (`update-ref refs/heads/<target> <expected_target_sha> <merge_sha>` → `restored`), falling back to `revertItem` when the target no longer equals `merge_sha` (throwaway clone, `git revert --no-edit -m 1 <merge_sha>` for a merge commit or `git revert --no-edit <merge_sha>` otherwise, publish with `update-ref` verifying the current head → `reverted`); end in `rolled_back`, or `failed` if an item could be neither restored nor reverted. On success set `committed`. Delete the `refs/novaforge/mergesets/<set>/*` refs in every terminal state. Publish `RefUpdate` events (`publishRefUpdate`) only after the set is `committed`.
- [ ] Implement `GetMergeSet` (org-scoped) and `RecoverMergeSets`: as a platform step at start, for each set in `committing`, if every item is `published` mark it `committed`, else continue the rollback exactly as `CommitMergeSet` does; for each set in `rolling_back`, finish the rollback. Call it from `cmd/git-platform/main.go` after migrations and before `grpcServer.Serve`, logging and continuing on error.
- [ ] Run the three tests; expect PASS. Remove the `restoreItem` loop, rerun, expect `TestMergeSetRestoresOnMovedTarget` to FAIL with "api main = …, want restored"; restore. Remove the `RecoverMergeSets` body (return nil), rerun, expect `TestMergeSetResumesAfterCrash` to FAIL with "want rolled_back or committed"; restore.
- [ ] Commit: `feat(gitops): all-or-nothing merge sets with a recoverable journal`.

## Task 7: Application-level Work Items

Files:

- `proto/novaforge/work/v1/work.proto` — `WorkItem.application_id = 17`, `CreateItemRequest.application_id = 7`, `ListItemsRequest.application_id = 3`.
- `internal/work/migrations/000011_application_items.up.sql` / `.down.sql`.
- `internal/work/store.go` — `Item.ApplicationID`; Create/scan/list.
- `internal/work/grpc.go` — `CreateItem` validates the application through `GitService.GetApplication`.

Interfaces (produced): `Item.ApplicationID uuid.UUID`; `func (s *Store) ListByApplication(ctx context.Context, applicationID uuid.UUID, state string) ([]Item, error)`. Consumed by Tasks 8, 9, 10.

- [ ] Write the failing test in `internal/work/store_test.go`:

```go
func TestApplicationWorkItemHasNoRepository(t *testing.T) {
	store := newStore(t)
	org, app := uuid.New(), uuid.New()
	ctx := scopedCtx(org)
	item, err := store.Create(ctx, work.Item{OrgID: org, ApplicationID: app, Type: "feature", Goal: "checkout across api and web"})
	if err != nil {
		t.Fatalf("create application item: %v", err)
	}
	if item.RepoID != uuid.Nil || item.ApplicationID != app {
		t.Fatalf("item = %+v, want application %s and no repository", item, app)
	}
	if _, err := store.Create(ctx, work.Item{OrgID: org, ApplicationID: app, RepoID: uuid.New(), Type: "feature", Goal: "both"}); err == nil {
		t.Fatal("an item with both a repository and an application was accepted")
	}
	listed, err := store.ListByApplication(ctx, app, "")
	if err != nil || len(listed) != 1 || listed[0].ID != item.ID {
		t.Fatalf("ListByApplication = %+v %v", listed, err)
	}
}
```

- [ ] Run `source hack/env.sh && go test ./internal/work/ -run TestApplicationWorkItemHasNoRepository -count=1`; expect FAIL with `unknown field ApplicationID in struct literal of type work.Item`.
- [ ] Add `000011_application_items.up.sql`:

```sql
ALTER TABLE work_items ALTER COLUMN repo_id DROP NOT NULL;
ALTER TABLE work_items ADD COLUMN IF NOT EXISTS application_id uuid NULL;
ALTER TABLE work_items ADD CONSTRAINT work_items_one_scope
    CHECK ((repo_id IS NULL) <> (application_id IS NULL));
CREATE INDEX IF NOT EXISTS work_items_application_idx ON work_items (org_id, application_id, state);
```

and `.down.sql` reversing it (deleting application items first). Add the proto fields, `make generate`. Store and scan `repo_id`/`application_id` through `*uuid.UUID`; implement `ListByApplication` with `WHERE org_id = $1 AND application_id = $2`. In `GRPCServer.CreateItem`, when `application_id` is set call `Git.GetApplication` with the caller's context and refuse `NotFound` if it fails; `ListItems` filters by `application_id` when set.

- [ ] Run the test; expect PASS. Remove the CHECK constraint, rerun, expect FAIL with "an item with both a repository and an application was accepted"; restore.
- [ ] Commit: `feat(work): Work Items that belong to an application`.

## Task 8: Change sets and their all-or-nothing merge in work-reviews

Files:

- `proto/novaforge/reviews/v1/reviews.proto` — `ChangeSet` message, `Run.change_set_id = 16`, five RPCs.
- `internal/reviews/migrations/000009_change_sets.up.sql` / `.down.sql`.
- `internal/reviews/changesets.go` — `ChangeSet`, store methods.
- `internal/reviews/changeset_merge.go` — `ChangeSetMerger`, `ReconcileMerging`.
- `internal/reviews/grpc_changesets.go` — RPC handlers.
- `cmd/work-reviews/main.go` — wire `ChangeSetMerger`, call `ReconcileMerging` at start.
- `internal/reviews/changesets_test.go` — two tests.

Interfaces:

```proto
message ChangeSet { string id = 1; string org_id = 2; string application_id = 3; int32 number = 4; string title = 5; string work_item_id = 6; string state = 7; string merge_set_id = 8; string failure = 9; repeated Run runs = 10; string created_at = 11; }
rpc CreateChangeSet(CreateChangeSetRequest) returns (CreateChangeSetResponse);       // {application_id, title, work_item_key, run_ids}
rpc GetChangeSet(GetChangeSetRequest) returns (GetChangeSetResponse);                // {id}
rpc ListChangeSets(ListChangeSetsRequest) returns (ListChangeSetsResponse);          // {application_id, state}
rpc AddRunToChangeSet(AddRunToChangeSetRequest) returns (AddRunToChangeSetResponse); // {id, run_id}
rpc MergeChangeSet(MergeChangeSetRequest) returns (MergeChangeSetResponse);          // {id, method} -> {change_set}
```

```go
type ChangeSetMerger struct {
	Store  *Store
	Gates  GateChecker
	Git    gitv1.GitServiceClient
}
func (m *ChangeSetMerger) Merge(ctx context.Context, id uuid.UUID, method string) (ChangeSet, error)
func (m *ChangeSetMerger) ReconcileMerging(ctx context.Context) error
var ErrChangeSetBlocked = errors.New("change set blocked")
```

Consumed by Tasks 9, 10, 11, 12.

- [ ] Write the failing tests `internal/reviews/changesets_test.go`:

```go
package reviews_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/reviews"
	"google.golang.org/grpc"
)

// appGit answers GetApplication with a fixed member list and records merge-set calls.
type appGit struct {
	gitv1.GitServiceClient
	members  []string
	prepared bool
}

func (g *appGit) GetApplication(_ context.Context, in *gitv1.GetApplicationRequest, _ ...grpc.CallOption) (*gitv1.GetApplicationResponse, error) {
	app := &gitv1.Application{Id: in.GetApplication(), Name: "shop"}
	for _, id := range g.members {
		app.Members = append(app.Members, &gitv1.Repo{Id: id, Name: "r-" + id[:4]})
	}
	return &gitv1.GetApplicationResponse{Application: app}, nil
}
func (g *appGit) PrepareMergeSet(context.Context, *gitv1.PrepareMergeSetRequest, ...grpc.CallOption) (*gitv1.PrepareMergeSetResponse, error) {
	g.prepared = true
	return &gitv1.PrepareMergeSetResponse{Set: &gitv1.MergeSet{Id: uuid.NewString(), State: "prepared"}}, nil
}
func (g *appGit) ListCommits(context.Context, *gitv1.ListCommitsRequest, ...grpc.CallOption) (*gitv1.ListCommitsResponse, error) {
	return &gitv1.ListCommitsResponse{Commits: []*gitv1.Commit{{Sha: reviewedSHA}}}, nil
}

func TestChangeSetGroupsRunsOfOneApplication(t *testing.T) {
	store := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	api, web, outside := uuid.New(), uuid.New(), uuid.New()
	git := &appGit{members: []string{api.String(), web.String()}}
	app := uuid.New()
	newRun := func(repo uuid.UUID) reviews.Run {
		r, err := store.CreateRun(ctx, reviews.Run{OrgID: org, RepoID: repo, Title: "checkout", SourceRef: "agents/NF-9/x", TargetRef: "main", AuthorID: uuid.New(), AuthorKind: "agent"})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	a, w, o, a2 := newRun(api), newRun(web), newRun(outside), newRun(api)
	cs, err := store.CreateChangeSet(ctx, git, reviews.ChangeSet{OrgID: org, ApplicationID: app, Title: "checkout"}, []uuid.UUID{a.ID, w.ID})
	if err != nil {
		t.Fatalf("create change set: %v", err)
	}
	if len(cs.Runs) != 2 {
		t.Fatalf("change set runs = %d, want 2", len(cs.Runs))
	}
	if _, err := store.AddRunToChangeSet(ctx, git, cs.ID, o.ID); err == nil || !strings.Contains(err.Error(), "not a member") {
		t.Fatalf("run of a non-member accepted: %v", err)
	}
	if _, err := store.AddRunToChangeSet(ctx, git, cs.ID, a2.ID); err == nil || !strings.Contains(err.Error(), "already has a run") {
		t.Fatalf("second run on the same repository accepted: %v", err)
	}
}

func TestMergeChangeSetRefusesWhenAnyRunIsBlocked(t *testing.T) {
	store := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	api, web := uuid.New(), uuid.New()
	git := &appGit{members: []string{api.String(), web.String()}}
	var ids []uuid.UUID
	for _, repo := range []uuid.UUID{api, web} {
		r, err := store.CreateRun(ctx, reviews.Run{OrgID: org, RepoID: repo, Title: "t", SourceRef: "feature", TargetRef: "main", AuthorID: uuid.New(), AuthorKind: "user"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	cs, err := store.CreateChangeSet(ctx, git, reviews.ChangeSet{OrgID: org, ApplicationID: uuid.New(), Title: "t"}, ids)
	if err != nil {
		t.Fatal(err)
	}
	// Gates pass everywhere, but no run has an independent approval.
	m := &reviews.ChangeSetMerger{Store: store, Gates: stubGateChecker{allowed: true}, Git: git}
	_, err = m.Merge(ctx, cs.ID, "merge")
	if !errors.Is(err, reviews.ErrChangeSetBlocked) || !strings.Contains(err.Error(), "approval") {
		t.Fatalf("merge without approvals: %v, want ErrChangeSetBlocked naming approval", err)
	}
	if git.prepared {
		t.Fatal("a merge set was prepared although a run was blocked")
	}
	got, err := store.GetChangeSet(ctx, cs.ID)
	if err != nil || got.State != "open" {
		t.Fatalf("change set after refusal = %+v %v, want open", got, err)
	}
}
```

- [ ] Run `source hack/env.sh && go test ./internal/reviews/ -run 'TestChangeSetGroupsRunsOfOneApplication|TestMergeChangeSetRefusesWhenAnyRunIsBlocked' -count=1`; expect FAIL with `store.CreateChangeSet undefined (type *reviews.Store has no field or method CreateChangeSet)`.
- [ ] Add `000009_change_sets.up.sql`:

```sql
CREATE TABLE IF NOT EXISTS change_sets (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    application_id uuid NOT NULL,
    number int NOT NULL,
    title text NOT NULL,
    work_item_id uuid NULL,
    state text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'merging', 'merged', 'closed', 'failed')),
    merge_set_id uuid NULL,
    failure text NOT NULL DEFAULT '',
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (application_id, number)
);
ALTER TABLE runs ADD COLUMN IF NOT EXISTS change_set_id uuid NULL REFERENCES change_sets (id) ON DELETE SET NULL;
CREATE UNIQUE INDEX IF NOT EXISTS runs_one_per_repo_per_change_set ON runs (change_set_id, repo_id) WHERE change_set_id IS NOT NULL;
```

and its `.down.sql`. Add the proto, `make generate`.

- [ ] Implement `CreateChangeSet(ctx, git gitv1.GitServiceClient, cs ChangeSet, runIDs []uuid.UUID)` and `AddRunToChangeSet(ctx, git, id, runID)`: org from scope; `git.GetApplication` with the caller's context gives the member ids; every run must be open, in the org, and its `RepoID` a member (`fmt.Errorf("run %s: repository %s is not a member of the application", ...)`); a second run on one repository is refused by the unique index (`"repository %s already has a run in this change set"`); `number` is `COALESCE(MAX(number),0)+1` per application. `GetChangeSet` and `ListChangeSets` (with runs) are org-scoped.
- [ ] Implement `ChangeSetMerger.Merge`: refuse unless `open`; for every run, the same checks as `Merger.Merge` — `Evaluate`, open, `sourceHead`, `refHead`, `MayMergePinned`, `hasIndependentApproval` — collecting every failure as `"<repo name>: <reason>"`; if any, return `fmt.Errorf("%w: %s", ErrChangeSetBlocked, strings.Join(reasons, "; "))` without calling git; else set `merging`, call `PrepareMergeSet` with each run's pinned `(source_ref, target_ref, method, head, target)` and `CommitMergeSet`, store `merge_set_id`; `committed` → mark every run `merged` and the set `merged`; `rolled_back` or `failed` → set `failed` with the merge set's `failure`, runs stay open. `ReconcileMerging` loads every change set in `merging` with a `merge_set_id`, calls `GetMergeSet` with a service context for that org, and applies the same mapping; wire it in `cmd/work-reviews/main.go` before serving.
- [ ] Run the two tests; expect PASS. Move the `PrepareMergeSet` call before the approval check, rerun, expect FAIL with "a merge set was prepared although a run was blocked"; restore.
- [ ] Commit: `feat(reviews): change sets merged all-or-nothing across an application`.

## Task 9: Agent Runs on an application-level Work Item

Files:

- `proto/novaforge/agents/v1/agents.proto` — `StartRunRequest.application_id = 8`, `Run.application_id`, `Run.change_set_id`.
- `internal/agents/migrations/000013_application_runs.up.sql` / `.down.sql` — `agent_runs.application_id uuid NULL`.
- `internal/agents/grpc.go` — StartRun for an application: members from `GitService.GetApplication`, grant `RepoIDs` = members the sponsor may write, run carries the application id.
- `internal/agentrun/engineering.go` — the engineering spec lists the application's repositories; a run that committed to several members opens one Engineering Run per touched repository and a change set over them (`CreateChangeSet`).
- `internal/gitops/credentials_test.go` — `TestAgentPushOutsideGrantRepos` (the transport-side proof).
- `tests/e2e/applications_test.sh` — the S-8 cluster part (Task 13).

Interfaces (produced): `StartRunRequest.application_id`; `agents.Run.ApplicationID uuid.UUID`; the agent prompt names every member by name and id; the result of an application run is a change set id on the run (`Run.change_set_id`). Consumed by Tasks 10 and 13.

- [ ] Write the failing transport test in `internal/gitops/credentials_test.go`:

```go
func TestAgentPushOutsideGrantRepos(t *testing.T) {
	org, a, b, agent := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	grants := staticGrants{capability.Grant{OrgID: org, SubjectID: agent, SubjectKind: "agent", WriteBranch: "agents/NF-5/", RepoIDs: []uuid.UUID{a}, ExpiresAt: time.Now().Add(time.Hour)}}
	ids := map[string]uuid.UUID{"api": a, "billing": b}
	capFn := gitops.NewGrantCapFunc(grants, func(_ context.Context, _ uuid.UUID, repo string) (uuid.UUID, error) { return ids[repo], nil })
	scope := authz.Scope{OrgID: org, ActorID: agent, ActorKind: "agent"}
	if err := capFn(context.Background(), scope, org, "api", []string{"refs/heads/agents/NF-5/x"}); err != nil {
		t.Fatalf("push to a member refused: %v", err)
	}
	if err := capFn(context.Background(), scope, org, "billing", []string{"refs/heads/agents/NF-5/x"}); err == nil {
		t.Fatal("push under the grant's prefix to a repository outside its set was allowed")
	}
}
```

with `type staticGrants []capability.Grant` and `func (s staticGrants) ListActive(context.Context, uuid.UUID, uuid.UUID) ([]capability.Grant, error) { return s, nil }` in the same file if not already present.

- [ ] Run `source hack/env.sh && go test ./internal/gitops/ -run TestAgentPushOutsideGrantRepos -count=1`; expect PASS once Task 3 is in (this test is Task 3's transport proof); remove `CanWriteRepo` from `requireGrants`, rerun, expect FAIL with "push under the grant's prefix to a repository outside its set was allowed"; restore.
- [ ] Write the failing agents test in `internal/agents/grpc_test.go`:

```go
func TestApplicationRunGrantNamesEveryMember(t *testing.T) {
	orgID, app := uuid.New(), uuid.New()
	api, web := uuid.New(), uuid.New()
	work := &stubWorkClient{item: &workv1.WorkItem{Id: uuid.New().String(), Key: "NF-9", ApplicationId: app.String(), State: "open"}}
	srv, store := newGRPCServer(t, work)
	srv.Git = &membersGit{members: []uuid.UUID{api, web}}
	ctx := scopedCtx(orgID)
	agent := mustCreateAgent(t, store, ctx, orgID)
	resp, err := srv.StartRun(ctx, &agentsv1.StartRunRequest{AgentId: agent.ID.String(), ApplicationId: app.String(), WorkItemKey: "NF-9", SponsorId: actorOf(ctx)})
	if err != nil {
		t.Fatalf("StartRun on an application: %v", err)
	}
	grant, err := capability.NewStore(grantsPool(t)).Resolve(scopedCtx(orgID), uuid.MustParse(resp.GetRun().GetGrantId()))
	if err != nil {
		t.Fatal(err)
	}
	if len(grant.RepoIDs) != 2 {
		t.Fatalf("grant repo_ids = %v, want exactly the two members", grant.RepoIDs)
	}
}
```

with `type membersGit struct{ gitv1.GitServiceClient; members []uuid.UUID }` answering `GetApplication` with those members. (`GRPCServer.Git` is the git-platform client field StartRun uses; if StartRun reaches git-platform through another field, use that field's name — `grep -n 'gitv1.GitServiceClient' internal/agents/grpc.go`.)

- [ ] Run `source hack/env.sh && go test ./internal/agents/ -run TestApplicationRunGrantNamesEveryMember -count=1`; expect FAIL with `unknown field ApplicationId in struct literal of type agentsv1.StartRunRequest`.
- [ ] Add the proto fields and migration, `make generate`. StartRun: exactly one of `repo_id`/`application_id` (`InvalidArgument` otherwise); for an application, the Work Item's `application_id` must equal it; members from `GetApplication` under the sponsor's context; refuse an application with no writable members (`FailedPrecondition "application %s has no repository the sponsor may write"`); grant `RepoIDs` = those members, `WriteBranch` unchanged; the branch lock is taken per member (`agent_runs` unique `(org_id, repo_id, branch)` — record the run under the first member and lock the branch name in each member through the existing `lock.go` path, one row per member).
- [ ] In `internal/agentrun/engineering.go`, for an application run, list the members in the run's instructions, and at completion open one Engineering Run per member with commits on the run's branch, then `CreateChangeSet(application, title=work item goal, work_item_key, run_ids)`; store its id on the agent run.
- [ ] Run `go test ./internal/agents/ ./internal/agentrun/ -count=1`; expect PASS. Remove `RepoIDs` from the application grant, rerun, expect FAIL with "want exactly the two members"; restore.
- [ ] Commit: `feat(agents): Agent Runs across an application, confined to its repositories`.

## Task 10: Edge REST, the overview fan-out, and OpenAPI

Files:

- `internal/edge/routes.go` — the routes listed in the spec's Interfaces.
- `internal/edge/app_handlers.go` — `addApplicationHandlers(h, git, ci, reviews, work, graph, agents)`.
- `internal/edge/app_overview.go` — the fan-out (`overviewConcurrency = 8`, per-member deadline 2.5 s).
- `internal/edge/handlers.go` — call `addApplicationHandlers`.
- `api/openapi.yaml` — regenerated with `make openapi`.
- `internal/edge/app_overview_test.go`, `internal/edge/coverage_test.go` — S-2 unit test and `TestOpenAPIListsEveryRoute`.

Interfaces (produced, consumed by Tasks 11–13): routes and operation ids:

```
GET    /api/v1/orgs/{org}/apps                              listApps
POST   /api/v1/orgs/{org}/apps                              createApp
GET    /api/v1/orgs/{org}/apps/{app}                        getApp
PATCH  /api/v1/orgs/{org}/apps/{app}                        updateApp
DELETE /api/v1/orgs/{org}/apps/{app}                        deleteApp
PUT    /api/v1/orgs/{org}/apps/{app}/repos/{repo}           addAppRepo      (body {"move": bool})
DELETE /api/v1/orgs/{org}/apps/{app}/repos/{repo}           removeAppRepo
GET    /api/v1/orgs/{org}/apps/{app}/overview               appOverview
GET    /api/v1/orgs/{org}/apps/{app}/search                 searchApp       (?q=&kind=code|knowledge)
GET    /api/v1/orgs/{org}/apps/{app}/releases               listAppReleases
POST   /api/v1/orgs/{org}/apps/{app}/releases               createAppRelease
GET    /api/v1/orgs/{org}/apps/{app}/releases/compare       compareAppReleases (?from=&to=)
GET    /api/v1/orgs/{org}/apps/{app}/releases/{version}     getAppRelease
GET    /api/v1/orgs/{org}/apps/{app}/work                   listAppWork
POST   /api/v1/orgs/{org}/apps/{app}/work                   createAppWork
GET    /api/v1/orgs/{org}/apps/{app}/work/{key}             getAppWork
GET    /api/v1/orgs/{org}/apps/{app}/changes                listChangeSets
POST   /api/v1/orgs/{org}/apps/{app}/changes                createChangeSet
GET    /api/v1/orgs/{org}/apps/{app}/changes/{id}           getChangeSet
POST   /api/v1/orgs/{org}/apps/{app}/changes/{id}/runs      addChangeSetRun
POST   /api/v1/orgs/{org}/apps/{app}/changes/{id}/merge     mergeChangeSet
POST   /api/v1/orgs/{org}/apps/{app}/agent-runs             startAppAgentRun
```

Overview JSON: `{"application": {...}, "state": "green|failing|unknown", "hidden_members": n, "members": [{"repo": {...}, "ci": {"state": "success|failure|running|none|unavailable", "run_id", "commit_sha"}, "open_runs": n|null, "latest_tag": "..."|null, "recent_commits": [...]|null, "unavailable": ["ci", ...]}]}`. Overall: `failing` if any member's CI is `failure`; else `unknown` if any member has `unavailable` data; else `green`.

- [ ] Write the failing tests. `internal/edge/app_overview_test.go`:

```go
package edge_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/edge"
)

type overviewGit struct{ gitv1.GitServiceClient }

func (overviewGit) GetApplication(context.Context, *gitv1.GetApplicationRequest, ...grpc.CallOption) (*gitv1.GetApplicationResponse, error) {
	return &gitv1.GetApplicationResponse{Application: &gitv1.Application{Id: "a1", Name: "shop", HiddenMembers: 1, Members: []*gitv1.Repo{
		{Id: "r1", Name: "api", DefaultBranch: "main"}, {Id: "r2", Name: "web", DefaultBranch: "main"},
	}}}, nil
}
func (overviewGit) ListTags(context.Context, *gitv1.ListTagsRequest, ...grpc.CallOption) (*gitv1.ListTagsResponse, error) {
	return &gitv1.ListTagsResponse{}, nil
}
func (overviewGit) ListCommits(context.Context, *gitv1.ListCommitsRequest, ...grpc.CallOption) (*gitv1.ListCommitsResponse, error) {
	return &gitv1.ListCommitsResponse{}, nil
}

type overviewCI struct{ civ1.CIServiceClient }

func (overviewCI) ListRuns(_ context.Context, in *civ1.ListRunsRequest, _ ...grpc.CallOption) (*civ1.ListRunsResponse, error) {
	if in.GetRepoId() == "r2" {
		return nil, errors.New("ci down for web")
	}
	return &civ1.ListRunsResponse{Runs: []*civ1.WorkflowRunSummary{{Id: "c1", Ref: "refs/heads/main", Status: "success"}}}, nil
}

type overviewReviews struct{ reviewsv1.ReviewsServiceClient }

func (overviewReviews) ListRuns(context.Context, *reviewsv1.ListRunsRequest, ...grpc.CallOption) (*reviewsv1.ListRunsResponse, error) {
	return &reviewsv1.ListRunsResponse{}, nil
}

func TestOverviewSaysUnavailableNotGreen(t *testing.T) {
	h := edge.Handlers(edge.Config{Git: overviewGit{}, CI: overviewCI{}, Reviews: overviewReviews{}})
	r := chi.NewRouter()
	r.Get("/api/v1/orgs/{org}/apps/{app}/overview", h["appOverview"])
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/apps/shop/overview", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		State         string `json:"state"`
		HiddenMembers int    `json:"hidden_members"`
		Members       []struct {
			Repo struct{ Name string } `json:"repo"`
			CI   struct{ State string } `json:"ci"`
		} `json:"members"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Members) != 2 {
		t.Fatalf("members = %d, want 2: a member whose data failed must not be dropped", len(body.Members))
	}
	states := map[string]string{}
	for _, m := range body.Members {
		states[m.Repo.Name] = m.CI.State
	}
	if states["api"] != "success" || states["web"] != "unavailable" {
		t.Fatalf("CI states = %v, want api success and web unavailable", states)
	}
	if body.State != "unknown" {
		t.Fatalf("overall state = %q, want unknown while a member is unavailable", body.State)
	}
	if body.HiddenMembers != 1 {
		t.Fatalf("hidden_members = %d, want 1", body.HiddenMembers)
	}
}
```

(Use the exact CI field names from `ci.proto` — `WorkflowRunSummary` status/ref fields — confirmed with `grep -n 'message WorkflowRunSummary' -A12 proto/novaforge/ci/v1/ci.proto` before writing; adjust the literal to them.)

In `internal/edge/coverage_test.go`:

```go
func TestOpenAPIListsEveryRoute(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"listApps", "createApp", "getApp", "updateApp", "deleteApp", "addAppRepo", "removeAppRepo",
		"appOverview", "searchApp", "listAppReleases", "createAppRelease", "compareAppReleases", "getAppRelease",
		"listAppWork", "createAppWork", "getAppWork", "listChangeSets", "createChangeSet", "getChangeSet",
		"addChangeSetRun", "mergeChangeSet", "startAppAgentRun"}
	routes := map[string]bool{}
	for _, rt := range edge.Routes() {
		routes[rt.OperationID] = true
	}
	for _, op := range want {
		if !routes[op] {
			t.Errorf("route %s is missing from the route table", op)
		}
		if !strings.Contains(string(raw), "operationId: "+op+"\n") {
			t.Errorf("route %s is missing from api/openapi.yaml; run make openapi", op)
		}
	}
}
```

(Use the route struct's actual operation-id field name from `routes.go`.)

- [ ] Run `go test ./internal/edge/ -run 'TestOverviewSaysUnavailableNotGreen|TestOpenAPIListsEveryRoute' -count=1`; expect FAIL with `route listApps is missing from the route table` and a nil-handler panic / 404 for `appOverview`.
- [ ] Add the routes; implement `addApplicationHandlers` — each handler calls the RPC of Tasks 2, 4, 5, 7, 8, 9 with `r.Context()` and maps errors with `StatusFromGRPC`; `searchApp` resolves members through `GetApplication` and calls `Graph.SearchCode`/`SearchKnowledge` with `RepoIds`, returning `{"mode", "hits": [{"repo": name, "path", ...}]}` and `503 {"error": "search is unavailable"}` when the graph call fails; `appOverview` fans out with a semaphore of 8, `context.WithTimeout(r.Context(), 2500*time.Millisecond)` per member, and records `"unavailable"` for each call that errs or times out. Wire it in `Handlers`. Run `make openapi`.
- [ ] Run both tests and `go test ./internal/edge/ -count=1`; expect PASS (including `TestEveryRouteIsInOpenAPI`, `TestRouterMountsEveryRoute`, `TestEveryRouteHasAHandlerWhenWired`). Make `appOverview` skip a member whose CI call failed, rerun, expect FAIL with "a member whose data failed must not be dropped"; restore.
- [ ] Commit: `feat(edge): application routes and the overview fan-out`.

## Task 11: `nf app` in the CLI

Files:

- `internal/cli/commands.go` — `"app": {"app", "Manage applications: repositories, releases and change sets", cmdApp}`.
- `internal/cli/app.go` — `cmdApp` and its subcommands.
- `internal/cli/app_test.go` — CLI tests against `stubEdge`.

Interfaces (produced, used by Task 13): `nf app create <name> [--description d]`, `nf app list`, `nf app show <app>`, `nf app rename <app> <new>`, `nf app delete <app>`, `nf app add <app> <repo> [--move]`, `nf app remove <app> <repo>`, `nf app status <app>`, `nf app search <app> <query> [--knowledge]`, `nf app release create <app> <version> [--name n] [--notes t]`, `nf app release list <app>`, `nf app release show <app> <version>`, `nf app release compare <app> <from> <to>`, `nf app change create <app> --title t [--work KEY] <run-number@repo>...`, `nf app change list <app>`, `nf app change show <app> <id>`, `nf app change merge <app> <id> [--method merge|squash|ff-only]`, `nf app work create <app> --type feature --goal g`, `nf app work list <app>`.

- [ ] Write the failing test `internal/cli/app_test.go`:

```go
package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/cli"
)

func TestAppCommandsCallTheirRoutes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seen := map[string]bool{}
	mark := func(key string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			seen[key] = true
			json.NewEncoder(w).Encode(map[string]any{"application": map[string]any{"name": "shop"}, "state": "green", "members": []any{}, "release": map[string]any{"version": "1.0.0"}, "change_set": map[string]any{"id": "c1", "state": "merged"}})
		}
	}
	srv := stubEdge(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v1/orgs/acme/apps":                      mark("create"),
		"PUT /api/v1/orgs/acme/apps/shop/repos/api":        mark("add"),
		"GET /api/v1/orgs/acme/apps/shop/overview":         mark("status"),
		"POST /api/v1/orgs/acme/apps/shop/releases":        mark("release"),
		"POST /api/v1/orgs/acme/apps/shop/changes/c1/merge": mark("merge"),
	})
	if err := cli.SaveConfig(cli.Config{Server: srv.URL, Token: "nf_t", Org: "acme"}); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"app", "create", "shop"},
		{"app", "add", "shop", "api"},
		{"app", "status", "shop"},
		{"app", "release", "create", "shop", "1.0.0"},
		{"app", "change", "merge", "shop", "c1"},
	} {
		var out, errb bytes.Buffer
		if code := cli.Execute(argv, &out, &errb); code != 0 {
			t.Fatalf("%s: exit %d: %s", strings.Join(argv, " "), code, errb.String())
		}
	}
	for _, k := range []string{"create", "add", "status", "release", "merge"} {
		if !seen[k] {
			t.Errorf("nf app did not call the %s route", k)
		}
	}
}
```

- [ ] Run `go test ./internal/cli/ -run TestAppCommandsCallTheirRoutes -count=1`; expect FAIL with `app create: exit 2: unknown command "app"` (the message `Execute` prints for an unknown command).
- [ ] Implement `cmdApp` following `cmdRepo`'s pattern (`session()`, `requireOrg(cfg)`, `c.Do(method, "/api/v1/orgs/"+org+"/apps"..., body, &out)`), printing tables with `text/tabwriter` as `cmdRepo list` does; `status` prints each member's CI state and the overall state, and prints `unavailable` verbatim.
- [ ] Run the test and `go test ./internal/cli/ -count=1`; expect PASS. Drop the `"app"` entry from `commands()`, rerun, expect FAIL; restore.
- [ ] Commit: `feat(cli): nf app`.

## Task 12: GUI — Applications

Files:

- `web/src/lib/api.ts` — typed calls for every route of Task 10.
- `web/src/screens/Applications.tsx` — the list and create form.
- `web/src/screens/app/AppPage.tsx` — tabs Overview, Repositories, Changes, Releases, Search, Work.
- `web/src/screens/app/ChangeSetPage.tsx` — runs, gates, approvals, one Merge button.
- `web/src/components/Rail.tsx` — "Applications" under BUILD, `to: "/apps"`.
- `web/src/App.tsx` — routes `/apps`, `/apps/:app/*`, `/apps/:app/changes/:id`.
- `web/src/screens/repo/RepoPage.tsx` — "Part of <app>" link when `application_id` is set.
- `web/tests/` — the existing browser test harness gains an applications scenario.

Interfaces: consumes the JSON shapes of Task 10; renders `Failed` (the existing component) for a request that failed and the literal text "unavailable" for a member field the overview marked unavailable; the Merge button is disabled and lists the blocking reasons from the last `mergeChangeSet` error or from the change set's run states.

- [ ] Write the failing type-level check: add to `web/tests/` a scenario file `applications.test.mjs` following the existing scenarios, which opens `/apps`, creates `shop`, adds two repositories, opens the Overview tab and asserts each member row shows a CI state, then opens a change set and asserts the Merge button names the blocking repository when one run is unapproved.
- [ ] Run `cd web && npx tsc -b --noEmit`; expect FAIL with `Cannot find module './screens/Applications'` once `App.tsx` references it (add the routes first).
- [ ] Implement the screens with the tokens in `src/theme.css`, the workspace scope from `src/lib/workspace.tsx` (the organization; never another org), and `Failed` for errors; Rail entry under BUILD between Repositories and Work.
- [ ] Run `cd web && npx tsc -b --noEmit && npm run build`; expect exit 0. Run the browser scenario with `EGO_BROWSER_SPACE=<space> npm test -- applications` against kw after Task 14; expect PASS.
- [ ] Commit: `feat(web): applications, change sets and composite releases`.

## Task 13: Applications e2e suite on kw, and gui/cli suite extensions

Files:

- `tests/e2e/applications_test.sh` — `TestApplicationOverviewOnTheCluster`, the kw half of the S-3 search criterion, `TestApplicationAgentRunCannotPushOutsideMembers`.
- `tests/e2e/gui_test.sh` — every new route answers through the edge.
- `tests/e2e/cli_test.sh` — `nf app` end to end.
- `CLAUDE.md` — the new suite in the e2e list.

Interfaces: run as `./hack/e2e-in-cluster.sh applications` (the runner executes `tests/e2e/<name>_test.sh`).

- [ ] Write `tests/e2e/applications_test.sh` with the header of `search_test.sh` (`set -euo pipefail`, `source hack/env.sh`, `KC`, `fail`, `ok`, `EDGE_IP`, `GIT_IP`, a fresh `XDG_CONFIG_HOME`, a unique user and org, `cleanup_org` trap), then:

```bash
echo "== TestApplicationOverviewOnTheCluster =="
/tmp/nf org create "$ORG" >/dev/null && /tmp/nf org use "$ORG" >/dev/null
/tmp/nf app create shop >/dev/null || fail "app create"
for r in api web worker; do
	/tmp/nf repo create "$r" >/dev/null || fail "repo create $r"
	/tmp/nf app add shop "$r" >/dev/null || fail "app add $r"
	git -C "$WORK" clone -q "http://$USER:$TOKEN@$GIT_IP:8081/$ORG/$r.git" "$r"
	mkdir -p "$WORK/$r/.novaforge" && cp tests/e2e/fixtures/ci-pass.yml "$WORK/$r/.novaforge/ci.yml"
	echo "func Gizmo${r^}() {}" >"$WORK/$r/gizmo.go"
	git -C "$WORK/$r" add -A && git -C "$WORK/$r" -c user.name=e2e -c user.email=e2e@x commit -qm init && git -C "$WORK/$r" push -q origin HEAD:main
done
for _ in $(seq 1 60); do
	ov="$(curl -fsS -H "$AUTH" "$API/apps/shop/overview")"
	n="$(echo "$ov" | python3 -c 'import json,sys;print(sum(1 for m in json.load(sys.stdin)["members"] if m["ci"]["state"] in ("success","failure")))')"
	[ "$n" = 3 ] && break
	sleep 5
done
[ "$n" = 3 ] || fail "overview never showed CI state for all 3 members: $ov"
ok "overview shows real CI state for every member"
# A collaborator granted only api sees one member and two hidden.
COLLAB="co$RANDOM$$"
curl -fsS -X POST "$BASE/api/v1/auth/register" -H 'Content-Type: application/json' -d "{\"email\":\"$COLLAB@example.com\",\"username\":\"$COLLAB\",\"password\":\"correct horse battery staple\"}" >/dev/null
curl -fsS -X PUT -H "$AUTH" "$API/repos/api/collaborators/$COLLAB" -H 'Content-Type: application/json' -d '{"role":"read"}' >/dev/null || fail "add collaborator"
CTOKEN="$(curl -fsS -X POST "$BASE/api/v1/auth/login" -H 'Content-Type: application/json' -d "{\"username\":\"$COLLAB\",\"password\":\"correct horse battery staple\"}" | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
hidden="$(curl -fsS -H "Authorization: Bearer $CTOKEN" "$API/apps/shop/overview" | python3 -c 'import json,sys;d=json.load(sys.stdin);print(len(d["members"]), d["hidden_members"])')"
[ "$hidden" = "1 2" ] || fail "collaborator overview = $hidden, want 1 member and 2 hidden"
ok "unreadable members are hidden and counted"

echo "== application search on the cluster (the kw half of the S-3 criterion) =="
/tmp/nf repo create outside >/dev/null || fail "repo create outside"
git -C "$WORK" clone -q "http://$USER:$TOKEN@$GIT_IP:8081/$ORG/outside.git" outside
echo "func GizmoOutside() {}" >"$WORK/outside/gizmo.go"
git -C "$WORK/outside" add -A && git -C "$WORK/outside" -c user.name=e2e -c user.email=e2e@x commit -qm init && git -C "$WORK/outside" push -q origin HEAD:main
for _ in $(seq 1 60); do
	hits="$(curl -fsS -H "$AUTH" "$API/apps/shop/search?q=Gizmo" | python3 -c 'import json,sys;print(" ".join(sorted({h["repo"] for h in json.load(sys.stdin)["hits"]})))')"
	[ "$hits" = "api web worker" ] && break
	sleep 5
done
[ "$hits" = "api web worker" ] || fail "application search found repos [$hits], want api web worker and never outside"
ok "application search spans exactly its members"

echo "== TestApplicationAgentRunCannotPushOutsideMembers =="
KEY="$(curl -fsS -X POST -H "$AUTH" "$API/apps/shop/work" -H 'Content-Type: application/json' -d '{"type":"feature","goal":"Add a Version() function to api and web returning \"1\""}' | python3 -c 'import json,sys;print(json.load(sys.stdin)["key"])')"
RUN="$(curl -fsS -X POST -H "$AUTH" "$API/apps/shop/agent-runs" -H 'Content-Type: application/json' -d "{\"work_item_key\":\"$KEY\",\"agent\":\"e2e-coder\"}" | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')"
for _ in $(seq 1 120); do
	st="$(curl -fsS -H "$AUTH" "$API/agent-runs/$RUN" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r["state"], r.get("change_set_id",""))')"
	case "$st" in succeeded*|failed*) break ;; esac
	sleep 10
done
case "$st" in "succeeded "?*) ;; *) fail "application agent run ended as: $st" ;; esac
CS="${st#succeeded }"
n="$(curl -fsS -H "$AUTH" "$API/apps/shop/changes/$CS" | python3 -c 'import json,sys;print(len(json.load(sys.stdin)["change_set"]["runs"]))')"
[ "$n" -ge 2 ] || fail "the agent's change set has $n runs, want at least 2"
# The run's grant must not reach "outside", even under the run's own branch prefix.
GRANT_TOKEN="$(curl -fsS -H "$AUTH" "$API/agent-runs/$RUN/credential" | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
BR="$(curl -fsS -H "$AUTH" "$API/agent-runs/$RUN" | python3 -c 'import json,sys;print(json.load(sys.stdin)["branch"])')"
git -C "$WORK/outside" checkout -qb "$BR" && git -C "$WORK/outside" -c user.name=a -c user.email=a@x commit -qm x --allow-empty
if git -C "$WORK/outside" push -q "http://agent:$GRANT_TOKEN@$GIT_IP:8081/$ORG/outside.git" "HEAD:$BR" 2>/dev/null; then
	fail "the application run's grant pushed to a repository outside the application"
fi
ok "an application Agent Run's grant is confined to the application's repositories"
```

(Before writing, confirm the actual route and JSON names the suite uses for collaborators, login, agent-run state, a run's credential and the CI fixture with `grep -n` in `internal/edge/routes.go`, `tests/e2e/agent_test.sh` and `tests/e2e/work_ci_test.sh`, and use those names.)

- [ ] Run `./hack/e2e-in-cluster.sh applications` against kw before Task 14's deploy; expect FAIL with `app create` (the deployed edge has no `/apps` route: 404).
- [ ] Extend `tests/e2e/gui_test.sh` with a request to every route of Task 10 (2xx for a created application), and `tests/e2e/cli_test.sh` with `nf app create/add/status/release create/release compare/change list`, each asserting exit 0 and its output; add `applications_test.sh` to CLAUDE.md's e2e list.
- [ ] Commit: `test(e2e): applications on the cluster`.

## Task 14: Deploy to kw and verify

Files:

- `deploy/helm/novaforge/values-kw.yaml` — the image tag (written by `hack/promote-kw.sh`).
- `BUILD-STATUS.md` — what was proven.

Interfaces: none new; consumes Tasks 1–13.

- [ ] Run the full suite: `source hack/env.sh && go test ./... -count=1`; expect `ok` for every package and no `FAIL`; `gofmt -l internal cmd` prints nothing; `cd web && npx tsc -b --noEmit && npm run build` exits 0; `make openapi && git diff --exit-code api/openapi.yaml`.
- [ ] Build and promote: `./hack/build-images.sh` then `./hack/promote-kw.sh $(git rev-parse --short HEAD)`; review the `values-kw.yaml` diff (only `image.tag`), commit `deploy: kw <tag>` and push; Kuvryn Sync applies it. Never `kubectl set image`.
- [ ] Wait until `kubectl --context kw -n novaforge get applications.sync.kuvryn.io novaforge` shows `Healthy Synced` at the pushed commit and every pod is `Running`; confirm the migrations applied: `kubectl --context kw -n novaforge logs deploy/novaforge-git-platform | grep -i migrat`.
- [ ] Run `./hack/e2e-in-cluster.sh applications gui cli merge work_ci`; expect every suite PASS.
- [ ] Record in `BUILD-STATUS.md` what the suites proved on kw, and close each todo with its evidence.
- [ ] Commit: `docs: applications proven on kw`.
