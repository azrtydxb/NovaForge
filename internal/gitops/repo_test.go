package gitops_test

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/gitops"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v — %s", strings.Join(args, " "), err, stderr.String())
	}
	return out.String()
}

func TestInitAndBranches(t *testing.T) {
	root := t.TempDir()
	orgID := uuid.New()

	repo, err := gitops.Init(root, orgID, "widgets")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Build a tree and commit using commit-tree against an empty tree.
	emptyTree := strings.TrimSpace(runGitWithStdin(t, repo.Path(), "", "mktree"))
	commitSHA := strings.TrimSpace(runGitWithStdin(t, repo.Path(), "",
		"commit-tree", emptyTree, "-m", "initial commit"))

	runGit(t, "", "--git-dir="+repo.Path(), "update-ref", "refs/heads/main", commitSHA)

	branches, err := repo.Branches()
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if len(branches) != 1 {
		t.Fatalf("want 1 branch, got %d: %+v", len(branches), branches)
	}
	if branches[0].Name != "main" {
		t.Fatalf("want branch named main, got %q", branches[0].Name)
	}
	if branches[0].SHA != commitSHA {
		t.Fatalf("want sha %s, got %s", commitSHA, branches[0].SHA)
	}

	log, err := repo.Log("main", 10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(log) != 1 || log[0].Message != "initial commit" {
		t.Fatalf("want 1 commit 'initial commit', got %+v", log)
	}
	if log[0].At.IsZero() {
		t.Fatal("want non-zero commit time")
	}
	if time.Since(log[0].At) > time.Hour {
		t.Fatalf("commit time too far in the past: %v", log[0].At)
	}

	opened, err := gitops.Open(root, orgID, "widgets")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened.Path() != repo.Path() {
		t.Fatalf("want same path, got %s vs %s", opened.Path(), repo.Path())
	}
}

func runGitWithStdin(t *testing.T, gitDir, stdin string, args ...string) string {
	t.Helper()
	full := append([]string{"--git-dir=" + gitDir}, args...)
	cmd := exec.Command("git", full...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v — %s", strings.Join(full, " "), err, stderr.String())
	}
	return out.String()
}

func TestPathTraversalRejected(t *testing.T) {
	root := t.TempDir()
	orgID := uuid.New()
	_, err := gitops.Open(root, orgID, "../escape")
	if err == nil || !strings.Contains(err.Error(), "invalid repository name") {
		t.Fatalf("want error containing 'invalid repository name', got %v", err)
	}
}

func TestTreeBlobAndDiff(t *testing.T) {
	root := t.TempDir()
	orgID := uuid.New()

	repo, err := gitops.Init(root, orgID, "docs")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Build a tree with one file "README.md" using a working checkout.
	work := t.TempDir()
	runGit(t, "", "clone", repo.Path(), work)
	writeFile(t, work+"/README.md", "hello world\n")
	runGit(t, work, "add", "README.md")
	runGit(t, work, "-c", "user.email=a@b.c", "-c", "user.name=Test", "commit", "-m", "add readme")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")

	entries, err := repo.Tree("main", "")
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "README.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want README.md in tree, got %+v", entries)
	}

	blob, err := repo.Blob("main", "README.md")
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if string(blob) != "hello world\n" {
		t.Fatalf("want blob content 'hello world\\n', got %q", blob)
	}

	// second commit for diff
	writeFile(t, work+"/README.md", "hello world\nmore\n")
	runGit(t, work, "add", "README.md")
	runGit(t, work, "-c", "user.email=a@b.c", "-c", "user.name=Test", "commit", "-m", "update readme")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")

	diff, err := repo.Diff("main~1", "main")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "more") {
		t.Fatalf("want diff to mention 'more', got %q", diff)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
