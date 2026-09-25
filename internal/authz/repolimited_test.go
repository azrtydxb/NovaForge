package authz_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/authz"
)

// An outside collaborator holds no organization membership: they were granted one
// repository and must reach nothing else in that organization.
//
// There are 35 files that authorize with RequireOrg, which compares the
// organization and nothing more. If a collaborator's scope carried the
// organization and passed RequireOrg, every one of those paths would admit them —
// Work Items, Engineering Runs, CI, the secrets listing, the graph. So the default
// is inverted here rather than at each call site: RequireOrg means "acting as a
// member of this organization" and refuses a repository-limited scope, which makes
// all 35 fail closed without being touched. A path that should admit a
// collaborator opts in with RequireRepo.
func TestRepoLimitedScopeIsRefusedByRequireOrg(t *testing.T) {
	org := uuid.New()
	repo := uuid.New()

	member := authz.WithScope(context.Background(), authz.Scope{
		OrgID: org, ActorID: uuid.New(), ActorKind: "user", Role: "member",
	})
	if err := authz.RequireOrg(member, org); err != nil {
		t.Fatalf("a member was refused their own organization: %v", err)
	}

	collaborator := authz.WithScope(context.Background(), authz.Scope{
		OrgID: org, ActorID: uuid.New(), ActorKind: "user",
		RepoLimited: true, Repos: []uuid.UUID{repo},
	})
	if err := authz.RequireOrg(collaborator, org); err == nil {
		t.Fatal("a repository-limited scope passed RequireOrg; every org-scoped path would admit it")
	}

	t.Run("RequireRepo admits the granted repository only", func(t *testing.T) {
		if err := authz.RequireRepo(collaborator, org, repo); err != nil {
			t.Fatalf("the collaborator was refused the repository they hold: %v", err)
		}
		other := uuid.New()
		if err := authz.RequireRepo(collaborator, org, other); err == nil {
			t.Fatal("the collaborator reached a repository they were not granted")
		}
		if err := authz.RequireRepo(collaborator, uuid.New(), repo); err == nil {
			t.Fatal("the collaborator reached their repository under another organization")
		}
	})

	t.Run("RequireRepo still admits an ordinary member", func(t *testing.T) {
		// A member's access is not repository-limited, so any repository in their
		// organization is theirs to reach as before.
		if err := authz.RequireRepo(member, org, uuid.New()); err != nil {
			t.Fatalf("a member was refused a repository in their organization: %v", err)
		}
	})

	t.Run("a repository-limited scope is not a member", func(t *testing.T) {
		s, err := authz.FromContext(collaborator)
		if err != nil {
			t.Fatal(err)
		}
		if s.IsOrgAdmin() {
			t.Fatal("a repository-limited scope reports itself an organization admin")
		}
		if s.Role != "" {
			t.Fatalf("a repository-limited scope carries role %q; it holds no membership role", s.Role)
		}
	})
}
