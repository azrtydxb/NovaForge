package work_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
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

// TestScanRepository pins the on-demand scan: unwired it says so rather than
// reporting a clean repository, only a person may ask, the scan runs in the
// caller's organization, and a scanner that could not run is reported.
func TestScanRepository(t *testing.T) {
	srv := newGRPCServer(t)
	orgID, repoID := uuid.New(), uuid.New()
	ctx := scopedCtx(orgID)
	req := &workv1.ScanRepositoryRequest{RepoId: repoID.String()}

	if _, err := srv.ScanRepository(ctx, req); status.Code(err) != codes.Unimplemented {
		t.Fatalf("unwired scan: code = %v, want Unimplemented", status.Code(err))
	}

	var gotOrg, gotRepo uuid.UUID
	srv.SetScanner(func(_ context.Context, o, r uuid.UUID) (work.ScanResult, error) {
		gotOrg, gotRepo = o, r
		return work.ScanResult{Findings: 2, ProposedKeys: []string{"NF-9"}, ScannerErrors: []string{"cve: osv-scanner is not installed"}}, nil
	})

	service := authz.WithScope(context.Background(), authz.Scope{OrgID: orgID, ActorKind: "service"})
	if _, err := srv.ScanRepository(service, req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("service caller: code = %v, want PermissionDenied", status.Code(err))
	}

	resp, err := srv.ScanRepository(ctx, req)
	if err != nil {
		t.Fatalf("ScanRepository: %v", err)
	}
	if gotOrg != orgID || gotRepo != repoID {
		t.Fatalf("scanned org %s repo %s, want %s %s", gotOrg, gotRepo, orgID, repoID)
	}
	if resp.GetFindings() != 2 || len(resp.GetProposedWorkItemKeys()) != 1 || len(resp.GetScannerErrors()) != 1 {
		t.Fatalf("response = %+v", resp)
	}
}
