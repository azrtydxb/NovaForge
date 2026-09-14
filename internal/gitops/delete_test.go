package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

func roleCtx(orgID uuid.UUID, kind, role string) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{
		OrgID: orgID, ActorID: uuid.New(), ActorKind: kind, Role: role,
	})
}

// TestDeleteRepoRequiresOwnerOrAdmin pins that deleting a repository — which
// destroys its history irrecoverably — is an owner's or admin's decision.
// DeleteRepo used to check only that the caller was in the organization, so
// any member, and any agent or platform service holding an org-scoped token,
// could erase a repository.
func TestDeleteRepoRequiresOwnerOrAdmin(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	orgID := uuid.New()
	name := "doomed-" + uuid.NewString()[:8]
	if _, err := srv.CreateRepo(roleCtx(orgID, "user", "owner"), &gitv1.CreateRepoRequest{Name: name}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	bare := filepath.Join(root, orgID.String(), name+".git")

	for _, refused := range []struct {
		kind, role string
	}{
		{"user", "member"},
		{"user", ""},
		{"agent", ""},
		{"service", ""},
		// A role on something that is not a person is not a person's role.
		{"agent", "owner"},
	} {
		_, err := srv.DeleteRepo(roleCtx(orgID, refused.kind, refused.role), &gitv1.DeleteRepoRequest{Name: name})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s with role %q: code = %v, want PermissionDenied", refused.kind, refused.role, status.Code(err))
		}
		if _, err := os.Stat(bare); err != nil {
			t.Fatalf("a refused delete still removed the bare repository: %v", err)
		}
		if _, err := srv.GetRepo(roleCtx(orgID, "user", "member"), &gitv1.GetRepoRequest{Name: name}); err != nil {
			t.Fatalf("a refused delete still removed the repository record: %v", err)
		}
	}

	for _, role := range []string{"admin", "owner"} {
		n := name + "-" + role
		if _, err := srv.CreateRepo(roleCtx(orgID, "user", "owner"), &gitv1.CreateRepoRequest{Name: n}); err != nil {
			t.Fatalf("CreateRepo: %v", err)
		}
		if _, err := srv.DeleteRepo(roleCtx(orgID, "user", role), &gitv1.DeleteRepoRequest{Name: n}); err != nil {
			t.Fatalf("DeleteRepo as %s: %v", role, err)
		}
		if _, err := os.Stat(filepath.Join(root, orgID.String(), n+".git")); !os.IsNotExist(err) {
			t.Fatalf("the bare repository survived deletion by an %s: %v", role, err)
		}
		if _, err := srv.GetRepo(roleCtx(orgID, "user", "owner"), &gitv1.GetRepoRequest{Name: n}); status.Code(err) != codes.NotFound {
			t.Fatalf("GetRepo after delete: code = %v, want NotFound", status.Code(err))
		}
		// The name is free again: a deleted repository must not leave a
		// directory behind that makes re-creating it fail.
		if _, err := srv.CreateRepo(roleCtx(orgID, "user", "owner"), &gitv1.CreateRepoRequest{Name: n}); err != nil {
			t.Fatalf("re-create after delete: %v", err)
		}
	}
}

// TestDeleteRepoIsOrgScoped pins the organization boundary: an owner of one
// organization naming another organization's repository must not reach it.
func TestDeleteRepoIsOrgScoped(t *testing.T) {
	srv, root := newGitGRPCServer(t)
	orgA, orgB := uuid.New(), uuid.New()
	name := "shared-name-" + uuid.NewString()[:8]
	if _, err := srv.CreateRepo(roleCtx(orgA, "user", "owner"), &gitv1.CreateRepoRequest{Name: name}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	_, err := srv.DeleteRepo(roleCtx(orgB, "user", "owner"), &gitv1.DeleteRepoRequest{Name: name})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("cross-org delete: code = %v, want NotFound", status.Code(err))
	}
	if _, err := os.Stat(filepath.Join(root, orgA.String(), name+".git")); err != nil {
		t.Fatalf("org A's repository was removed by org B's owner: %v", err)
	}
}
