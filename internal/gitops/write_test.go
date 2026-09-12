package gitops_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

func TestCreateBranchFromDefault(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	name := "wr-" + uuid.NewString()[:8]
	if _, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: name}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	seedInitialCommit(t, filepath.Join(root, orgID.String(), name+".git"))

	resp, err := srv.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: name, Name: "feature/x"})
	if err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if resp.GetRef().GetSha() == "" {
		t.Fatal("want a non-empty sha for the new branch")
	}

	list, err := srv.ListBranches(ctx, &gitv1.ListBranchesRequest{Repo: name})
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	var found bool
	for _, r := range list.GetRefs() {
		if r.GetName() == "feature/x" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the new branch is not listed: %v", list.GetRefs())
	}

	// Creating it twice must not silently repoint an existing branch.
	if _, err := srv.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: name, Name: "feature/x"}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("want codes.AlreadyExists on a second create, got %v", err)
	}
}

func TestCreateCommitWritesAndDeletesFiles(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	name := "wr-" + uuid.NewString()[:8]
	if _, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: name}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	seedInitialCommit(t, filepath.Join(root, orgID.String(), name+".git"))

	resp, err := srv.CreateCommit(ctx, &gitv1.CreateCommitRequest{
		Repo:    name,
		Branch:  "main",
		Message: "agent: add a file",
		Files: []*gitv1.FileChange{
			{Path: "pkg/new.txt", Content: []byte("written by an agent\n")},
			{Path: "README.md", Deleted: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateCommit: %v", err)
	}
	if resp.GetSha() == "" {
		t.Fatal("want a non-empty commit sha")
	}

	blob, err := srv.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: name, Ref: "main", Path: "pkg/new.txt"})
	if err != nil {
		t.Fatalf("GetBlob for the written file: %v", err)
	}
	if string(blob.GetContent()) != "written by an agent\n" {
		t.Fatalf("the committed content did not round-trip: %q", blob.GetContent())
	}
	if _, err := srv.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: name, Ref: "main", Path: "README.md"}); status.Code(err) != codes.NotFound {
		t.Fatalf("want the deleted file to be gone, got %v", err)
	}
}

// TestCreateCommitRefusesEscapingPaths pins that a path supplied by an agent
// cannot write outside the working tree or into the repository's own git
// directory — the two ways a file change becomes remote code execution.
func TestCreateCommitRefusesEscapingPaths(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	name := "wr-" + uuid.NewString()[:8]
	if _, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: name}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	seedInitialCommit(t, filepath.Join(root, orgID.String(), name+".git"))

	for _, path := range []string{"../escaped.txt", ".git/hooks/pre-commit", "/etc/passwd"} {
		_, err := srv.CreateCommit(ctx, &gitv1.CreateCommitRequest{
			Repo: name, Branch: "main", Message: "x",
			Files: []*gitv1.FileChange{{Path: path, Content: []byte("x")}},
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("path %q: want codes.InvalidArgument, got %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "escaped.txt")); err == nil {
		t.Fatal("a refused path still wrote a file outside the repository")
	}
}

// TestCreateCommitRefusesANoOp pins that a commit which changes nothing is
// refused rather than recorded: an empty commit claims work that did not
// happen.
func TestCreateCommitRefusesANoOp(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)

	name := "wr-" + uuid.NewString()[:8]
	if _, err := srv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: name}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	seedInitialCommit(t, filepath.Join(root, orgID.String(), name+".git"))

	_, err := srv.CreateCommit(ctx, &gitv1.CreateCommitRequest{
		Repo: name, Branch: "main", Message: "no change",
		Files: []*gitv1.FileChange{{Path: "README.md", Content: []byte("hello\n")}},
	})
	if err == nil {
		t.Fatal("want an error for a commit that changes nothing")
	}
}
