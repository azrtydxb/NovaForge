package gitops_test

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

func TestCrossRepositoryDiffDoesNotImportObjects(t *testing.T) {
	srv, root := forkTestServer(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	parent, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	commit := func(repo, path, body string) string {
		t.Helper()
		r, err := srv.CreateCommit(ctx, &gitv1.CreateCommitRequest{Repo: repo, Branch: "main", Message: "change", Files: []*gitv1.FileChange{{Path: path, Content: []byte(body)}}})
		if err != nil {
			t.Fatal(err)
		}
		return r.GetSha()
	}
	base := commit(parent.GetRepo().GetId(), "README", "original\n")
	fork, err := srv.ForkRepo(ctx, &gitv1.ForkRepoRequest{Repo: parent.GetRepo().GetId(), Name: "fork"})
	if err != nil {
		t.Fatal(err)
	}
	head := commit(fork.GetRepo().GetId(), "migrations/a\tb.sql", "select 1;\n")
	// The parent advances after the fork. A two-dot comparison would wrongly
	// classify the parent's addition as a removal made by the contributor.
	commit(parent.GetRepo().GetId(), "parent-only", "upstream\n")
	request := &gitv1.GetDiffRequest{Repo: parent.GetRepo().GetId(), SourceRepo: fork.GetRepo().GetId(), From: "main", To: head, MergeBase: true}
	diff, err := srv.GetDiff(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.GetPathsComplete() || !slices.Equal(diff.GetChangedPaths(), []string{"migrations/a\tb.sql"}) || strings.Contains(diff.GetUnified(), "parent-only") {
		t.Fatalf("wrong cross-fork diff: %v", diff)
	}
	parentPath := filepath.Join(root, org.String(), "parent.git")
	if err := exec.Command("git", "--git-dir="+parentPath, "cat-file", "-e", head).Run(); err == nil {
		t.Fatal("diff imported fork objects into parent")
	}
	if _, err := srv.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: fork.GetRepo().GetId(), Ref: base, Path: "README"}); err != nil {
		t.Fatal(err)
	}
	other := uuid.New()
	foreign, err := srv.CreateRepo(scopedCtx(other), &gitv1.CreateRepoRequest{Name: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	request.SourceRepo = foreign.GetRepo().GetId()
	if _, err := srv.GetDiff(ctx, request); err == nil {
		t.Fatal("cross-organization source admitted")
	}
	request.SourceRepo = fork.GetRepo().GetId()
	request.To = "--output=somewhere"
	if _, err := srv.GetDiff(ctx, request); err == nil {
		t.Fatal("Git option admitted as source ref")
	}
}
