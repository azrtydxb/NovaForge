package gitops_test

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gitops"
)

// TestForkCarriesHistoryAndIsolatesWrites pins the two properties that make a
// fork a fork rather than a pointer: it is a full repository holding the
// parent's history, and writing to it cannot touch the parent. The obvious
// cheap implementation — an alternates file or a shared object store with a
// separate ref namespace — gives the first property and quietly breaks the
// second, so both are asserted here, and the write is a real `git push` from a
// clone rather than an API call, because a push is what a contributor does.
func TestForkCarriesHistoryAndIsolatesWrites(t *testing.T) {
	srv, root := forkTestServer(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	parentName := "upstream-" + uuid.NewString()[:8]
	parent, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: parentName})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	first, err := srv.CreateCommit(ctx, &gitv1.CreateCommitRequest{
		Repo: parent.GetRepo().GetId(), Branch: "main", Message: "upstream history",
		Files: []*gitv1.FileChange{{Path: "README.md", Content: []byte("upstream\n")}},
	})
	if err != nil {
		t.Fatalf("seed parent history: %v", err)
	}

	forkName := "fork-" + uuid.NewString()[:8]
	forked, err := srv.ForkRepo(ctx, &gitv1.ForkRepoRequest{Repo: parent.GetRepo().GetId(), Name: forkName})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if got := forked.GetRepo().GetParentRepoId(); got != parent.GetRepo().GetId() {
		t.Fatalf("fork records parent %q, want %q", got, parent.GetRepo().GetId())
	}
	if forked.GetRepo().GetId() == parent.GetRepo().GetId() {
		t.Fatal("fork must be its own repository, not the parent")
	}

	// The parent's commit has to be reachable in the fork itself — a fork that
	// answered only by proxying to the parent would break the moment the parent
	// was archived, renamed or deleted.
	commits, err := srv.ListCommits(ctx, &gitv1.ListCommitsRequest{Repo: forked.GetRepo().GetId(), Ref: "main", Limit: 10})
	if err != nil {
		t.Fatalf("list fork commits: %v", err)
	}
	if len(commits.GetCommits()) != 1 || commits.GetCommits()[0].GetSha() != first.GetSha() {
		t.Fatalf("fork history = %v, want the parent's commit %s", commits.GetCommits(), first.GetSha())
	}
	forkPath := filepath.Join(root, orgID.String(), forkName+".git")
	if _, err := os.Stat(forkPath); err != nil {
		t.Fatalf("fork has no repository on disk: %v", err)
	}
	// A fork whose objects are borrowed from the parent's object store is not a
	// full repository: deleting the parent would empty it.
	if _, err := os.Stat(filepath.Join(forkPath, "objects", "info", "alternates")); err == nil {
		t.Fatal("fork borrows the parent's object store; it must carry its own copy")
	}

	// A push to the fork, done with the git binary exactly as a contributor's
	// would be.
	clone := t.TempDir()
	gitRun(t, "", "clone", forkPath, clone)
	if err := os.WriteFile(filepath.Join(clone, "contribution.txt"), []byte("from the fork\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, clone, "add", "contribution.txt")
	gitRun(t, clone, "-c", "user.name=Fork", "-c", "user.email=fork@localhost", "commit", "-m", "fork work")
	gitRun(t, clone, "push", "origin", "HEAD:refs/heads/contribution")

	forkRefs, err := srv.ListBranches(ctx, &gitv1.ListBranchesRequest{Repo: forked.GetRepo().GetId()})
	if err != nil {
		t.Fatalf("list fork branches: %v", err)
	}
	if !hasBranch(forkRefs.GetRefs(), "contribution") {
		t.Fatalf("fork branches = %v, want the pushed branch", forkRefs.GetRefs())
	}

	parentRefs, err := srv.ListBranches(ctx, &gitv1.ListBranchesRequest{Repo: parent.GetRepo().GetId()})
	if err != nil {
		t.Fatalf("list parent branches: %v", err)
	}
	if hasBranch(parentRefs.GetRefs(), "contribution") {
		t.Fatal("a push to the fork created a branch in the parent")
	}
	if len(parentRefs.GetRefs()) != 1 || parentRefs.GetRefs()[0].GetSha() != first.GetSha() {
		t.Fatalf("parent refs = %v, want only main at %s", parentRefs.GetRefs(), first.GetSha())
	}
}

func hasBranch(refs []*gitv1.Ref, name string) bool {
	for _, r := range refs {
		if r.GetName() == name {
			return true
		}
	}
	return false
}

// forkTestServer builds a server on a throwaway PostgreSQL database of its own.
//
// The package's other tests use the cluster's shared dev database, and this test
// cannot: that database is shared with every other lane building against this
// cluster, and its gitplatform_git tracking table records whatever migration
// version another lane pushed last. golang-migrate then refuses the whole set
// ("no migration found for version 5") for a reason that has nothing to do with
// forks. Creating and dropping a database here keeps that failure out of this
// test, and keeps this test's repositories invisible to everyone else's.
// internal/webhooks/store_test.go does the same, and says the same.
func forkTestServer(t *testing.T) (*gitops.Server, string) {
	t.Helper()
	admin := gitopsDBURL(t)
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect to create a test database: %v", err)
	}
	defer owner.Close(ctx)

	name := "forks_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := owner.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		// A fresh connection: the pool above is closed by its own cleanup, and
		// dropping a database needs a connection that is not to it.
		dropCtx := context.Background()
		conn, err := pgx.Connect(dropCtx, admin)
		if err != nil {
			return
		}
		defer conn.Close(dropCtx)
		_, _ = conn.Exec(dropCtx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	})

	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	u.Path = "/" + name
	if err := database.MigrateAs(u.String(), "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform schema: %v", err)
	}
	pool, err := database.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	root := t.TempDir()
	return gitops.NewGRPCServer(pool, root), root
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v — %s", strings.Join(args, " "), err, out)
	}
}
