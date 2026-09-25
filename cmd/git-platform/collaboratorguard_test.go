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

// S-26: a person who is not a member of an organization can be granted access to one
// repository in it, reaches only that repository, and loses access when the grant is
// removed.
//
// This drives an unmodified git client over the real transport, because that is where
// the rule has to hold. Identity refuses a non-member at credential resolution by
// default, so the transport asks for a resolution that admits one and reports them as
// a non-member; the scope that comes back is repository-limited, which authz.RequireOrg
// refuses everywhere, and the transport confines it to the grants it carries.
func TestOutsideCollaboratorReachesOnlyItsRepository(t *testing.T) {
	p := platformtest.Start(t)
	owner := p.NewUser(t, "collabowner")
	outsider := p.NewUser(t, "outsider")
	org := p.NewOrg(t, owner, "collaborg")
	granted := p.NewRepo(t, owner, org, "granted", nil)
	private := p.NewRepo(t, owner, org, "private", nil)
	orgID := uuid.MustParse(org.ID)

	// Seed both repositories so a clone has something to fetch.
	for _, name := range []string{granted.Name, private.Name} {
		seedRepo(t, p, org, owner, name)
	}

	caps := newCapFunc(p.Grants,
		func(context.Context, authz.Scope, uuid.UUID, string, []string) error { return nil },
		newArchiveGuard(p.Pool),
		newCollaboratorGuard(repoIDResolver(p.Pool)))
	httpSrv := httptest.NewServer(gitops.NewHTTPHandler(p.GitRoot,
		gitops.NewCredentialAuthFunc(p.Identity, platformtest.HMACSecret, p.Collaborators), caps))
	defer httpSrv.Close()

	url := func(repo string) string {
		return fmt.Sprintf("http://x:%s@%s/%s/%s.git", outsider.Session, httpSrv.Listener.Addr(), org.Name, repo)
	}

	t.Run("with no grant, nothing is reachable", func(t *testing.T) {
		out, err := gitIn(t, t.TempDir(), "clone", url(granted.Name), t.TempDir())
		if err == nil {
			t.Fatalf("a non-member with no grant cloned the repository: %s", out)
		}
	})

	grantedID := uuid.MustParse(granted.ID)
	if err := p.Collaborators.AddCollaborator(context.Background(), orgID, grantedID, uuid.MustParse(outsider.ID), uuid.Nil, "write"); err != nil {
		t.Fatalf("AddCollaborator: %v", err)
	}

	work := t.TempDir()
	t.Run("the granted repository is clonable and pushable", func(t *testing.T) {
		mustGit(t, "", "clone", "-q", url(granted.Name), work)
		mustGit(t, work, "-c", "user.email=o@x.c", "-c", "user.name=O", "commit", "-q", "--allow-empty", "-m", "an outsider's change")
		mustGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
	})

	t.Run("a second repository in the same organization is not", func(t *testing.T) {
		out, err := gitIn(t, t.TempDir(), "clone", url(private.Name), t.TempDir())
		if err == nil {
			t.Fatalf("the collaborator cloned a repository they were not granted: %s", out)
		}
	})

	t.Run("a read grant does not write", func(t *testing.T) {
		readOnly := p.NewRepo(t, owner, org, "readonly", nil)
		seedRepo(t, p, org, owner, readOnly.Name)
		if err := p.Collaborators.AddCollaborator(context.Background(), orgID,
			uuid.MustParse(readOnly.ID), uuid.MustParse(outsider.ID), uuid.Nil, "read"); err != nil {
			t.Fatalf("AddCollaborator read: %v", err)
		}
		ro := t.TempDir()
		mustGit(t, "", "clone", "-q", url(readOnly.Name), ro)
		mustGit(t, ro, "-c", "user.email=o@x.c", "-c", "user.name=O", "commit", "-q", "--allow-empty", "-m", "should not land")
		out, err := gitIn(t, ro, "push", "origin", "HEAD:refs/heads/main")
		if err == nil {
			t.Fatalf("a read grant accepted a push: %s", out)
		}
		if !strings.Contains(out, "read access") {
			t.Fatalf("the push was refused, but not for the read-only grant: %s", out)
		}
	})

	t.Run("revoking removes the access", func(t *testing.T) {
		if err := p.Collaborators.RemoveCollaborator(context.Background(), orgID, grantedID,
			uuid.MustParse(outsider.ID), uuid.Nil); err != nil {
			t.Fatalf("RemoveCollaborator: %v", err)
		}
		out, err := gitIn(t, t.TempDir(), "clone", url(granted.Name), t.TempDir())
		if err == nil {
			t.Fatalf("a revoked grant still clones: %s", out)
		}
	})
}

// seedRepo puts one commit on a repository's default branch, as its owner.
func seedRepo(t *testing.T, p *platformtest.Platform, org platformtest.Org, owner platformtest.User, repo string) {
	t.Helper()
	dir := t.TempDir()
	remote := fmt.Sprintf("%s/%s/%s.git", withCredential(p.GitHTTPURL, owner.Session), org.Name, repo)
	mustGit(t, "", "clone", "-q", remote, dir)
	mustGit(t, dir, "-c", "user.email=a@b.c", "-c", "user.name=A", "commit", "-q", "--allow-empty", "-m", "initial")
	mustGit(t, dir, "push", "-q", "origin", "HEAD:refs/heads/main")
}

// withCredential puts a credential in a base URL, because git will not prompt for
// one in a test.
func withCredential(base, credential string) string {
	return strings.Replace(base, "http://", "http://x:"+credential+"@", 1)
}
