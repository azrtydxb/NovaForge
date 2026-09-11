package work_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/work"
)

func newGRPCServer(t *testing.T) *work.GRPCServer {
	t.Helper()
	return work.NewGRPCServer(newStore(t))
}

func TestCreateItemAllocatesKeyOverGRPC(t *testing.T) {
	srv := newGRPCServer(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	resp, err := srv.CreateItem(ctx, &workv1.CreateItemRequest{
		RepoId: repoID.String(),
		Type:   "feature",
		Goal:   "ship it",
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if resp.GetItem().GetKey() == "" {
		t.Fatalf("CreateItem response has no allocated key: %+v", resp)
	}
	if resp.GetItem().GetState() != "open" {
		t.Fatalf("CreateItem state = %q, want open", resp.GetItem().GetState())
	}
}

// TestGetItemDeniedForForeignOrg asserts that a Work Item created in one
// organization cannot be read by a caller scoped to a different
// organization — the org boundary is enforced by GetItem itself, not just
// by the store.
func TestGetItemDeniedForForeignOrg(t *testing.T) {
	srv := newGRPCServer(t)
	orgA := uuid.New()
	orgB := uuid.New()
	repoID := uuid.New()

	created, err := srv.CreateItem(scopedCtx(orgA), &workv1.CreateItemRequest{
		RepoId: repoID.String(),
		Type:   "feature",
		Goal:   "org a's item",
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	_, err = srv.GetItem(scopedCtx(orgB), &workv1.GetItemRequest{Id: created.GetItem().GetId()})
	if err == nil {
		t.Fatal("GetItem across orgs: want error, got nil")
	}
	if status.Code(err) != codes.NotFound && status.Code(err) != codes.PermissionDenied {
		t.Fatalf("GetItem across orgs: code = %v, want NotFound or PermissionDenied", status.Code(err))
	}
}

// TestListItemsRequiresScope asserts a call with no authorization scope in
// context is denied rather than defaulting to some unrestricted view.
func TestListItemsRequiresScope(t *testing.T) {
	srv := newGRPCServer(t)
	_, err := srv.ListItems(context.Background(), &workv1.ListItemsRequest{RepoId: uuid.New().String()})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ListItems with no scope: code = %v, want PermissionDenied", status.Code(err))
	}
}
