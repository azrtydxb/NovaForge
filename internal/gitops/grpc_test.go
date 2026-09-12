package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gitops"
)

func gitopsDBURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return u
}

func newGitGRPCServer(t *testing.T) (*gitops.Server, string) {
	t.Helper()
	url := gitopsDBURL(t)
	if err := database.MigrateAs(url, "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform schema: %v", err)
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	root := t.TempDir()
	return gitops.NewGRPCServer(pool, root), root
}

func scopedCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID:     orgID,
		ActorID:   uuid.New(),
		ActorKind: "user",
	})
}

func TestCreateRepoInitialisesBareRepo(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	resp, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: "demo-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	headPath := filepath.Join(root, orgID.String(), resp.GetRepo().GetName()+".git", "HEAD")
	if _, err := os.Stat(headPath); err != nil {
		t.Fatalf("want %s to exist, got %v", headPath, err)
	}
	if resp.GetRepo().GetDefaultBranch() != "main" {
		t.Fatalf("want default branch main, got %q", resp.GetRepo().GetDefaultBranch())
	}
}

func TestListReposIsOrgScoped(t *testing.T) {
	srv, _ := newGitGRPCServer(t)
	orgA := uuid.New()
	orgB := uuid.New()

	if _, err := srv.CreateRepo(scopedCtx(orgA), &gitv1.CreateRepoRequest{Name: "a-repo-" + uuid.NewString()[:8]}); err != nil {
		t.Fatalf("create repo in org A: %v", err)
	}
	if _, err := srv.CreateRepo(scopedCtx(orgB), &gitv1.CreateRepoRequest{Name: "b-repo-" + uuid.NewString()[:8]}); err != nil {
		t.Fatalf("create repo in org B: %v", err)
	}

	listA, err := srv.ListRepos(scopedCtx(orgA), &gitv1.ListReposRequest{})
	if err != nil {
		t.Fatalf("list repos for org A: %v", err)
	}
	for _, r := range listA.GetRepos() {
		if r.GetOrgId() != orgA.String() {
			t.Fatalf("org A's repo list leaked a repository from org %s", r.GetOrgId())
		}
	}
	if len(listA.GetRepos()) != 1 {
		t.Fatalf("want exactly 1 repo for org A, got %d", len(listA.GetRepos()))
	}
}

func TestGetBlobUnknownPath(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	name := "demo-" + uuid.NewString()[:8]
	if _, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: name}); err != nil {
		t.Fatalf("create repo: %v", err)
	}

	repoPath := filepath.Join(root, orgID.String(), name+".git")
	seedInitialCommit(t, repoPath)

	_, err := srv.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: name, Ref: "main", Path: "does-not-exist.txt"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("want codes.NotFound, got %v", err)
	}
}

func TestMergeFastForward(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	name := "demo-" + uuid.NewString()[:8]
	if _, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: name}); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	repoPath := filepath.Join(root, orgID.String(), name+".git")
	seedInitialCommit(t, repoPath)

	// Clone, branch, and push a feature branch with a new commit.
	work := t.TempDir()
	runGit(t, "", "clone", repoPath, work)
	runGit(t, work, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, work, "add", "feature.txt")
	runGit(t, work, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-m", "add feature")
	runGit(t, work, "push", "origin", "feature")

	resp, err := srv.Merge(ctx, &gitv1.MergeRequest{
		Repo:      name,
		SourceRef: "feature",
		TargetRef: "main",
		Method:    "merge",
		Message:   "merge feature",
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if resp.GetMergeSha() == "" {
		t.Fatal("want a non-empty merge sha")
	}

	list, err := srv.ListCommits(ctx, &gitv1.ListCommitsRequest{Repo: name, Ref: "main", Limit: 10})
	if err != nil {
		t.Fatalf("list commits: %v", err)
	}
	if len(list.GetCommits()) < 2 {
		t.Fatalf("want at least 2 commits on main after merge, got %d", len(list.GetCommits()))
	}
}

// seedCommit creates an initial commit on main in the bare repository at
// repoPath, via a throwaway clone, so read RPCs have something to read.
func seedInitialCommit(t *testing.T, repoPath string) {
	t.Helper()
	work := t.TempDir()
	runGit(t, "", "clone", repoPath, work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, work, "add", "README.md")
	runGit(t, work, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-m", "initial commit")
	runGit(t, work, "push", "origin", "HEAD:main")
}

// TestRepoResolvableByIDAndName pins a defect that silently disabled all of CI:
// the scheduler carries a repository id from the push event it is handling,
// but reads resolved only by name, so every GetBlob returned NotFound and the
// scheduler's "no workflow here" branch swallowed it.
func TestRepoResolvableByIDAndName(t *testing.T) {
	srv, _ := newGitGRPCServer(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	created, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: "byref"})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	id := created.GetRepo().GetId()

	byName, err := srv.GetRepo(ctx, &gitv1.GetRepoRequest{Name: "byref"})
	if err != nil {
		t.Fatalf("GetRepo by name: %v", err)
	}
	byID, err := srv.GetRepo(ctx, &gitv1.GetRepoRequest{Name: id})
	if err != nil {
		t.Fatalf("GetRepo by id: %v", err)
	}
	if byName.GetRepo().GetId() != byID.GetRepo().GetId() {
		t.Fatalf("name and id resolved to different repositories: %s vs %s",
			byName.GetRepo().GetId(), byID.GetRepo().GetId())
	}

	// The read path the scheduler actually uses must accept an id too.
	if _, err := srv.ListBranches(ctx, &gitv1.ListBranchesRequest{Repo: id}); err != nil {
		t.Fatalf("ListBranches by id: %v", err)
	}
}
