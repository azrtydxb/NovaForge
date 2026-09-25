package gitops_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// A repository could only be created and deleted: it could not be renamed, its
// default branch could not be changed, it could not be archived and it could not
// be transferred. A team moving onto NovaForge from another Git host gives all of
// that up, and forks need rename and default branch to exist first.
//
// Paths are root/<org>/<name>.git, so a rename and a transfer move the directory.
// The move happens with the row so a failure cannot leave the two disagreeing.
func TestRepositoryAdministration(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	created, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: "ledger"})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	id := created.GetRepo().GetId()
	path := filepath.Join(root, orgID.String(), "ledger.git")
	seedInitialCommit(t, path)
	// A second branch to make the default branch change observable.
	work := t.TempDir()
	runGit(t, "", "clone", path, work)
	runGit(t, work, "checkout", "-b", "trunk")
	runGit(t, work, "push", "origin", "trunk")

	t.Run("rename", func(t *testing.T) {
		if _, err := srv.UpdateRepo(ctx, &gitv1.UpdateRepoRequest{Repo: id, Name: "accounts"}); err != nil {
			t.Fatalf("UpdateRepo rename: %v", err)
		}
		renamed := filepath.Join(root, orgID.String(), "accounts.git")
		if _, err := os.Stat(renamed); err != nil {
			t.Fatalf("the renamed repository is not on disk: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("the old path still exists after a rename: %v", err)
		}
		into := t.TempDir()
		runGit(t, "", "clone", renamed, filepath.Join(into, "clone"))
		path = renamed
	})

	t.Run("default branch", func(t *testing.T) {
		if _, err := srv.UpdateRepo(ctx, &gitv1.UpdateRepoRequest{Repo: id, DefaultBranch: "trunk"}); err != nil {
			t.Fatalf("UpdateRepo default branch: %v", err)
		}
		got, err := srv.GetRepo(ctx, &gitv1.GetRepoRequest{Name: "accounts"})
		if err != nil {
			t.Fatalf("GetRepo: %v", err)
		}
		if got.GetRepo().GetDefaultBranch() != "trunk" {
			t.Fatalf("default branch is %q, want trunk", got.GetRepo().GetDefaultBranch())
		}
		into := filepath.Join(t.TempDir(), "clone")
		runGit(t, "", "clone", path, into)
		head := strings.TrimSpace(runGit(t, into, "rev-parse", "--abbrev-ref", "HEAD"))
		if head != "trunk" {
			t.Fatalf("a fresh clone checked out %q, want trunk", head)
		}
	})

	t.Run("archive refuses writes and still serves reads", func(t *testing.T) {
		if _, err := srv.UpdateRepo(ctx, &gitv1.UpdateRepoRequest{Repo: id, Archived: true, SetArchived: true}); err != nil {
			t.Fatalf("UpdateRepo archive: %v", err)
		}
		if _, err := srv.ListBranches(ctx, &gitv1.ListBranchesRequest{Repo: id}); err != nil {
			t.Fatalf("an archived repository stopped serving reads: %v", err)
		}
		_, err := srv.Merge(ctx, &gitv1.MergeRequest{Repo: id, SourceRef: "main", TargetRef: "trunk", Method: "merge"})
		if err == nil {
			t.Fatal("an archived repository accepted a merge")
		}
		if !strings.Contains(err.Error(), "archived") {
			t.Fatalf("the refusal does not name the archive: %v", err)
		}
	})

	t.Run("transfer", func(t *testing.T) {
		if _, err := srv.UpdateRepo(ctx, &gitv1.UpdateRepoRequest{Repo: id, Archived: false, SetArchived: true}); err != nil {
			t.Fatalf("un-archive: %v", err)
		}
		receiving := uuid.New()
		if _, err := srv.TransferRepo(ctx, &gitv1.TransferRepoRequest{Repo: id, ToOrg: receiving.String()}); err != nil {
			t.Fatalf("TransferRepo: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, receiving.String(), "accounts.git")); err != nil {
			t.Fatalf("the transferred repository is not under the receiving organization: %v", err)
		}
		mine, err := srv.ListRepos(ctx, &gitv1.ListReposRequest{})
		if err != nil {
			t.Fatalf("ListRepos: %v", err)
		}
		for _, r := range mine.GetRepos() {
			if r.GetId() == id {
				t.Fatal("the transferred repository is still listed in the sending organization")
			}
		}
		theirs, err := srv.ListRepos(scopedCtx(receiving), &gitv1.ListReposRequest{})
		if err != nil {
			t.Fatalf("ListRepos receiving: %v", err)
		}
		found := false
		for _, r := range theirs.GetRepos() {
			if r.GetId() == id {
				found = true
			}
		}
		if !found {
			t.Fatal("the transferred repository is not listed in the receiving organization")
		}
	})
}
