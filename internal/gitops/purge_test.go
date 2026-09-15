package gitops_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/events"
)

// TestDeleteRepoAnnouncesTheDeletion pins the event every other service
// cleans up on. Deleting a repository used to publish nothing: its Work
// Items, CI runs, reviews, index and agent runs stayed in their services'
// schemas, unreachable and never removed. The announcement comes before the
// repository is removed, and a deletion that cannot be announced does not
// happen — the alternative is a repository gone with its data left behind
// and nothing to say so.
func TestDeleteRepoAnnouncesTheDeletion(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	orgID := uuid.New()
	owner := roleCtx(orgID, "user", "owner")
	name := "announced-" + uuid.NewString()[:8]
	created, err := srv.CreateRepo(owner, &gitv1.CreateRepoRequest{Name: name})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	srv.SetRepoDeletedPublisher(func(context.Context, events.RepoDeletedEvent) error {
		return errors.New("redis is down")
	})
	if _, err := srv.DeleteRepo(owner, &gitv1.DeleteRepoRequest{Name: name}); status.Code(err) != codes.Unavailable {
		t.Fatalf("delete with no way to announce it: code = %v, want Unavailable", status.Code(err))
	}
	if _, err := os.Stat(filepath.Join(root, orgID.String(), name+".git")); err != nil {
		t.Fatalf("an unannounced delete still removed the repository: %v", err)
	}

	var got []events.RepoDeletedEvent
	srv.SetRepoDeletedPublisher(func(_ context.Context, e events.RepoDeletedEvent) error {
		got = append(got, e)
		return nil
	})
	if _, err := srv.DeleteRepo(owner, &gitv1.DeleteRepoRequest{Name: name}); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("published %d deletion events, want 1", len(got))
	}
	if got[0].OrgID != orgID || got[0].RepoID.String() != created.GetRepo().GetId() || got[0].RepoName != name {
		t.Fatalf("event = %+v, want org %s repo %s %s", got[0], orgID, created.GetRepo().GetId(), name)
	}
}

// TestPurgeOrganizationRemovesRepositories covers git-platform's share of an
// organization's deletion: its repository records, its capability grants and
// its bare repositories on disk — and nothing of another organization.
func TestPurgeOrganizationRemovesRepositories(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	doomed, kept := uuid.New(), uuid.New()
	for _, org := range []uuid.UUID{doomed, kept} {
		if _, err := srv.CreateRepo(roleCtx(org, "user", "owner"), &gitv1.CreateRepoRequest{Name: "r-" + uuid.NewString()[:8]}); err != nil {
			t.Fatalf("CreateRepo: %v", err)
		}
	}

	if err := srv.PurgeOrganization(roleCtx(doomed, "service", "")); err != nil {
		t.Fatalf("PurgeOrganization: %v", err)
	}
	if list, err := srv.ListRepos(roleCtx(doomed, "user", "owner"), &gitv1.ListReposRequest{}); err != nil || len(list.GetRepos()) != 0 {
		t.Fatalf("repositories left after the purge: %v, %v", list.GetRepos(), err)
	}
	if _, err := os.Stat(filepath.Join(root, doomed.String())); !os.IsNotExist(err) {
		t.Fatalf("the organization's repositories are still on disk: %v", err)
	}
	if list, err := srv.ListRepos(roleCtx(kept, "user", "owner"), &gitv1.ListReposRequest{}); err != nil || len(list.GetRepos()) != 1 {
		t.Fatalf("another organization's repository was touched: %v, %v", list.GetRepos(), err)
	}
	if _, err := os.Stat(filepath.Join(root, kept.String())); err != nil {
		t.Fatalf("another organization's repositories left the disk: %v", err)
	}
}
