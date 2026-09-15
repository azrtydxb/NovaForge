package edge_test

import (
	"context"
	"net/http"
	"testing"

	"google.golang.org/grpc"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/edge"
)

type identityDeleteDouble struct {
	identityv1.IdentityServiceClient
	got *identityv1.DeleteOrgRequest
}

func (d *identityDeleteDouble) DeleteOrg(_ context.Context, in *identityv1.DeleteOrgRequest, _ ...grpc.CallOption) (*identityv1.DeleteOrgResponse, error) {
	d.got = in
	return &identityv1.DeleteOrgResponse{Org: &identityv1.Org{Id: "org-1", Name: in.GetOrg()}}, nil
}

// TestDeleteOrgCarriesTheConfirmation pins the route an organization's owner
// deletes it through, and that the name typed to confirm reaches identity,
// which refuses a deletion that does not name the organization.
func TestDeleteOrgCarriesTheConfirmation(t *testing.T) {
	id := &identityDeleteDouble{}
	h := edge.Handlers(edge.Config{Identity: id})
	rec := call(t, h, "deleteOrg", http.MethodDelete, `{"confirm_name":"acme"}`, map[string]string{"org": "acme"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if id.got == nil || id.got.GetOrg() != "acme" || id.got.GetConfirmName() != "acme" {
		t.Fatalf("DeleteOrg got %+v, want org acme confirmed as acme", id.got)
	}
}
