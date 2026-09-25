package gates

import (
	"context"
	"errors"

	"github.com/google/uuid"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/secrets"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CredentialStatus preserves retryability without returning provider bodies.
// Shared issuance/redemption RPCs use this instead of flattening outages into
// permission denials. Authorization errors still fail closed.
func CredentialStatus(err error) error {
	switch {
	case errors.Is(err, secrets.ErrProductionScoped):
		return status.Error(codes.PermissionDenied, "production-scoped credential not permitted")
	case errors.Is(err, secrets.ErrProviderUnavailable):
		return status.Error(codes.Unavailable, "credential provider unavailable")
	case errors.Is(err, secrets.ErrHardExpiryUnavailable), errors.Is(err, secrets.ErrProviderNotConfigured), errors.Is(err, secrets.ErrProviderContract), errors.Is(err, secrets.ErrUnderlyingCredentialNotRevocable), errors.Is(err, secrets.ErrScopeClosed):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Error(codes.PermissionDenied, "credential operation denied")
	}
}

func (g *GRPCServer) credentialService(ctx context.Context) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || scope.ActorKind != "service" || scope.ServiceName != "ci-credentials" {
		return status.Error(codes.PermissionDenied, "credential lifecycle requires an organization-scoped platform service")
	}
	if g.Secrets == nil {
		return status.Error(codes.Unimplemented, "credential broker not configured")
	}
	return nil
}

func (g *GRPCServer) RevokeRunLeases(ctx context.Context, req *gatesv1.RevokeRunLeasesRequest) (*gatesv1.RevokeRunLeasesResponse, error) {
	if err := g.credentialService(ctx); err != nil {
		return nil, err
	}
	run, err := parseUUID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	if run == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	var attempt uuid.UUID
	if req.GetAttemptId() != "" {
		attempt, err = parseUUID("attempt_id", req.GetAttemptId())
		if err != nil {
			return nil, err
		}
		if attempt == uuid.Nil {
			return nil, status.Error(codes.InvalidArgument, "attempt_id must be nonzero when supplied")
		}
	}
	result, err := g.Secrets.RevokeRunLeases(ctx, run, attempt)
	// A durable fence remains useful even when the issuer is unavailable.
	// Pending is explicit: a successful RPC does NOT claim provider cleanup.
	if result.Fenced {
		return &gatesv1.RevokeRunLeasesResponse{Fenced: true, Pending: int32(result.Pending)}, nil
	}
	if err != nil {
		return nil, CredentialStatus(err)
	}
	return nil, status.Error(codes.Internal, "credential fence not established")
}

func (g *GRPCServer) RetryCredentialRevocations(ctx context.Context, _ *gatesv1.RetryCredentialRevocationsRequest) (*gatesv1.RetryCredentialRevocationsResponse, error) {
	if err := g.credentialService(ctx); err != nil {
		return nil, err
	}
	if err := g.Secrets.RetryRevocations(ctx); err != nil {
		return nil, CredentialStatus(err)
	}
	return &gatesv1.RetryCredentialRevocationsResponse{}, nil
}
