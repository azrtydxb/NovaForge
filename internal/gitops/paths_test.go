package gitops_test

import (
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/gitops"
)

func TestTreePreservesLiteralGitNames(t *testing.T) {
	repo, err := gitops.Init(t.TempDir(), uuid.New(), "literal-paths")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	runGit(t, "", "clone", repo.Path(), work)
	wanted := map[string]bool{"space name.go": false, "tab\tname.go": false, "line\nname.go": false, "café.go": false, "quote\"name.go": false}
	for name := range wanted {
		writeFile(t, filepath.Join(work, name), "package p\n")
	}
	runGit(t, work, "add", "--all")
	runGit(t, work, "-c", "user.email=a@b.c", "-c", "user.name=Test", "commit", "-m", "literal names")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	entries, err := repo.Tree("main", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if _, ok := wanted[entry.Name]; !ok {
			t.Errorf("tree invented an escaped filename: %q", entry.Name)
			continue
		}
		wanted[entry.Name] = true
		if body, err := repo.Blob("main", entry.Name); err != nil || string(body) != "package p\n" {
			t.Errorf("tree name cannot retrieve its blob: %q %v", entry.Name, err)
		}
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("tree omitted literal filename %q", name)
		}
	}
}
