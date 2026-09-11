package gitops_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/gitops"
)

func TestCloneAndPushOverHTTP(t *testing.T) {
	root := t.TempDir()
	orgID := uuid.New()

	if _, err := gitops.Init(root, orgID, "widgets"); err != nil {
		t.Fatalf("Init: %v", err)
	}

	auth := func(ctx context.Context, user, pass string) (authz.Scope, error) {
		if user != "user" || pass != "token" {
			return authz.Scope{}, fmt.Errorf("invalid credentials")
		}
		return authz.Scope{OrgID: orgID, ActorID: uuid.New(), ActorKind: "user"}, nil
	}
	caps := func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		return nil
	}

	handler := gitops.NewHTTPHandler(root, auth, caps)
	server := httptest.NewServer(handler)
	defer server.Close()

	repoURL := fmt.Sprintf("http://user:token@%s/%s/widgets.git", server.Listener.Addr().String(), orgID.String())

	work := t.TempDir()
	runCmd(t, "", "git", "clone", repoURL, work)
	writeFileHTTP(t, work+"/hello.txt", "hi\n")
	runCmd(t, work, "git", "add", "hello.txt")
	runCmd(t, work, "git", "-c", "user.email=a@b.c", "-c", "user.name=Test", "commit", "-m", "add hello")
	runCmd(t, work, "git", "push", "origin", "HEAD:refs/heads/main")

	repo, err := gitops.Open(root, orgID, "widgets")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	log, err := repo.Log("main", 10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	found := false
	for _, c := range log {
		if c.Message == "add hello" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want pushed commit in log, got %+v", log)
	}
}

func TestPushDeniedByCapability(t *testing.T) {
	root := t.TempDir()
	orgID := uuid.New()

	if _, err := gitops.Init(root, orgID, "guarded"); err != nil {
		t.Fatalf("Init: %v", err)
	}

	auth := func(ctx context.Context, user, pass string) (authz.Scope, error) {
		return authz.Scope{OrgID: orgID, ActorID: uuid.New(), ActorKind: "user"}, nil
	}
	caps := func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		for _, r := range refs {
			if r == "refs/heads/main" {
				return fmt.Errorf("write to %s not permitted", r)
			}
		}
		return nil
	}

	handler := gitops.NewHTTPHandler(root, auth, caps)
	server := httptest.NewServer(handler)
	defer server.Close()

	repoURL := fmt.Sprintf("http://user:token@%s/%s/guarded.git", server.Listener.Addr().String(), orgID.String())

	work := t.TempDir()
	runCmd(t, "", "git", "clone", repoURL, work)
	writeFileHTTP(t, work+"/hello.txt", "hi\n")
	runCmd(t, work, "git", "add", "hello.txt")
	runCmd(t, work, "git", "-c", "user.email=a@b.c", "-c", "user.name=Test", "commit", "-m", "add hello")

	cmd := exec.Command("git", "push", "origin", "HEAD:refs/heads/main")
	cmd.Dir = work
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("want push to fail, got success: %s", out)
	}
	if !strings.Contains(string(out), "not permitted") {
		t.Fatalf("want stderr to contain 'not permitted', got %s", out)
	}
}

func runCmd(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v — %s", name, strings.Join(args, " "), err, out)
	}
}

func writeFileHTTP(t *testing.T, path, content string) {
	t.Helper()
	writeFile(t, path, content)
}
