package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/platformtest"
)

// Archiving a repository has to refuse a real push, not just a Merge RPC: the
// transports are how anyone actually writes. Both get the check from the CapFunc
// they already share, so this proves the wiring as well as the rule — a guard
// only the RPC consulted would leave every push working.
func TestArchivedRepositoryRefusesAPush(t *testing.T) {
	p := platformtest.Start(t)
	owner := p.NewUser(t, "archowner")
	org := p.NewOrg(t, owner, "archorg")
	repo := p.NewRepo(t, owner, org, "frozen", nil)
	orgID := uuid.MustParse(org.ID)

	caps := newCapFunc(p.Grants, func(context.Context, authz.Scope, uuid.UUID, string, []string) error { return nil }, newArchiveGuard(p.Pool), newCollaboratorGuard(repoIDResolver(p.Pool)), gitops.NewMirrorCapFunc(p.Pool))
	auth := func(ctx context.Context, user, pass, orgRef string) (authz.Scope, error) {
		return authz.Scope{OrgID: orgID, ActorID: uuid.MustParse(owner.ID), ActorKind: "user"}, nil
	}
	httpSrv := httptest.NewServer(gitops.NewHTTPHandler(p.GitRoot, auth, caps))
	defer httpSrv.Close()
	// Credentials go in the URL because git will not prompt in a test; the path
	// addresses the organization by id, as the transport's own tests do.
	remote := fmt.Sprintf("http://person:x@%s/%s/%s.git", httpSrv.Listener.Addr(), orgID, repo.Name)

	work := t.TempDir()
	mustGit(t, "", "clone", "-q", remote, work)
	mustGit(t, work, "-c", "user.email=a@b.c", "-c", "user.name=A", "commit", "-q", "--allow-empty", "-m", "before archiving")
	mustGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")

	// Archive it, then try the same push again.
	if _, err := p.Pool.Exec(context.Background(),
		`UPDATE gitplatform.repositories SET archived = true WHERE org_id = $1 AND name = $2`,
		orgID, repo.Name); err != nil {
		t.Fatalf("archive: %v", err)
	}
	mustGit(t, work, "-c", "user.email=a@b.c", "-c", "user.name=A", "commit", "-q", "--allow-empty", "-m", "after archiving")
	out, err := gitIn(t, work, "push", "origin", "HEAD:refs/heads/main")
	if err == nil {
		t.Fatalf("an archived repository accepted a push: %s", out)
	}
	if !strings.Contains(out, "archived") {
		t.Fatalf("the push was refused, but not for the archive: %s", out)
	}

	// A clone still works: archiving stops history moving, it does not hide it.
	into := t.TempDir()
	mustGit(t, "", "clone", "-q", remote, into)
}
