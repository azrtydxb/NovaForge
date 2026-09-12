package gates

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// ListSecrets returns the secrets registered for the caller's organization,
// names and environments only. Nothing here decrypts anything: a person may
// legitimately ask what exists without being handed the material.
func (g *GRPCServer) ListSecrets(ctx context.Context, _ *gatesv1.ListSecretsRequest) (*gatesv1.ListSecretsResponse, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "no authorization scope for this call")
	}
	if g.Secrets == nil {
		return nil, status.Error(codes.Unimplemented, "this deployment brokers no secrets")
	}
	found, err := g.Secrets.List(ctx, scope.OrgID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list secrets: %v", err)
	}
	out := make([]*gatesv1.SecretReference, 0, len(found))
	for _, s := range found {
		out = append(out, &gatesv1.SecretReference{Name: s.Name, Environment: s.Environment})
	}
	return &gatesv1.ListSecretsResponse{Secrets: out}, nil
}

// ListLeases returns the credentials currently brokered to runs.
func (g *GRPCServer) ListLeases(ctx context.Context, _ *gatesv1.ListLeasesRequest) (*gatesv1.ListLeasesResponse, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "no authorization scope for this call")
	}
	if g.Secrets == nil {
		return nil, status.Error(codes.Unimplemented, "this deployment brokers no secrets")
	}
	found, err := g.Secrets.ListLeases(ctx, scope.OrgID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list leases: %v", err)
	}
	out := make([]*gatesv1.Lease, 0, len(found))
	for _, l := range found {
		out = append(out, &gatesv1.Lease{
			Id:         l.ID.String(),
			SecretName: l.SecretName,
			RunId:      l.RunID.String(),
			State:      l.State,
			ExpiresAt:  l.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return &gatesv1.ListLeasesResponse{Leases: out}, nil
}

// RevokeLease ends a live lease immediately. A credential that turns out to
// have been issued wrongly has to be stoppable before it expires, or its TTL
// is the only bound on the mistake.
func (g *GRPCServer) RevokeLease(ctx context.Context, req *gatesv1.RevokeLeaseRequest) (*gatesv1.RevokeLeaseResponse, error) {
	if g.Secrets == nil {
		return nil, status.Error(codes.Unimplemented, "this deployment brokers no secrets")
	}
	id, err := parseUUID("id", req.GetId())
	if err != nil {
		return nil, err
	}
	// Revoke carries the caller's organization predicate, so another
	// organization's lease id simply finds nothing to revoke.
	if err := g.Secrets.Revoke(ctx, id); err != nil {
		return nil, status.Errorf(codes.NotFound, "revoke lease: %v", err)
	}
	return &gatesv1.RevokeLeaseResponse{Revoked: true}, nil
}
