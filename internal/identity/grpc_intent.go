package identity

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"time"
)

func runtimeOwner(ctx context.Context) (authz.Scope, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || scope.ActorKind != "service" || scope.ServiceName != "agent-runtime" {
		return authz.Scope{}, status.Error(codes.PermissionDenied, "agent-runtime service required")
	}
	return scope, nil
}
func (s *Server) validatedIntent(ctx context.Context, p *identityv1.GrantIntent, cleanup bool) (capability.IssuanceIntent, error) {
	scope, err := runtimeOwner(ctx)
	if err != nil {
		return capability.IssuanceIntent{}, err
	}
	i, err := capability.IntentFromProto(p)
	if err != nil {
		return i, status.Error(codes.InvalidArgument, err.Error())
	}
	if i.Grant.OrgID != scope.OrgID || i.IssuerKind != "user" {
		return i, status.Error(codes.PermissionDenied, "intent outside caller scope")
	}
	if cleanup {
		stored, e := s.grants.StoredIntent(ctx, i.Grant.ID)
		if e == nil {
			if !proto.Equal(capability.IntentProto(stored), capability.IntentProto(i)) {
				return i, status.Error(codes.PermissionDenied, "intent mismatch")
			}
			return i, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return i, status.Error(codes.Internal, "grant receipt unavailable")
		}
	}
	if s.Agents == nil {
		return i, status.Error(codes.Unavailable, "run owner unavailable")
	}
	token, err := svcauth.Mint(s.HMACSecret, "identity", scope.OrgID, svcauth.DefaultTTL)
	if err != nil {
		return i, status.Error(codes.Internal, "run owner authentication unavailable")
	}
	call, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), 5*time.Second)
	defer cancel()
	receipt, err := s.Agents.GetGrantIntent(call, &agentsv1.GetGrantIntentRequest{RunId: i.RunID.String()})
	if err != nil {
		return i, status.Error(codes.Unavailable, "run owner validation unavailable")
	}
	if !proto.Equal(receipt.GetIntent(), capability.IntentProto(i)) {
		return i, status.Error(codes.PermissionDenied, "intent differs from run owner")
	}
	if !cleanup {
		member, err := s.store.IsOrgMember(ctx, scope.OrgID, i.IssuerID)
		if err != nil {
			return i, status.Error(codes.Internal, "issuer membership unavailable")
		}
		if !member {
			return i, status.Error(codes.PermissionDenied, "issuer is not an organization member")
		}
	}
	return i, nil
}
func intentError(err error) error {
	if errors.Is(err, capability.ErrIssuanceDenied) {
		return status.Error(codes.PermissionDenied, "grant intent denied")
	}
	return status.Error(codes.Internal, "grant authority unavailable")
}
func (s *Server) IssueIntent(ctx context.Context, r *identityv1.IssueIntentRequest) (*identityv1.IssueIntentResponse, error) {
	i, err := s.validatedIntent(ctx, r.GetIntent(), false)
	if err != nil {
		return nil, err
	}
	grant, err := s.grants.IssueVerifiedRunIntent(ctx, i)
	if err != nil {
		return nil, intentError(err)
	}
	return &identityv1.IssueIntentResponse{Grant: capability.GrantProto(grant)}, nil
}
func (s *Server) CancelIssuance(ctx context.Context, r *identityv1.CancelIssuanceRequest) (*identityv1.CancelIssuanceResponse, error) {
	i, err := s.validatedIntent(ctx, r.GetIntent(), true)
	if err != nil {
		return nil, err
	}
	if err = s.grants.CancelVerifiedRunIntent(ctx, i); err != nil {
		return nil, intentError(err)
	}
	return &identityv1.CancelIssuanceResponse{Ok: true}, nil
}
func (s *Server) RevokeGrant(ctx context.Context, r *identityv1.RevokeGrantRequest) (*identityv1.RevokeGrantResponse, error) {
	scope, authErr := runtimeOwner(ctx)
	if authErr != nil {
		return nil, authErr
	}
	id, err := uuid.Parse(r.GetId())
	if err != nil || id == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid grant id")
	}
	// A run owner receipt is mandatory: generic id-only revocation cannot be
	// used to claim a never-issued id is fenced against delayed issuance.
	intent, err := s.grants.StoredIntent(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		binding, e := s.grants.LegacyCancellation(ctx, id)
		if errors.Is(e, pgx.ErrNoRows) {
			if s.Agents == nil {
				return nil, status.Error(codes.Unavailable, "run owner unavailable")
			}
			token, e := svcauth.Mint(s.HMACSecret, "identity", scope.OrgID, svcauth.DefaultTTL)
			if e != nil {
				return nil, status.Error(codes.Internal, "run owner authentication unavailable")
			}
			call, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), 5*time.Second)
			defer cancel()
			receipt, e := s.Agents.GetLegacyGrantBinding(call, &agentsv1.GetLegacyGrantBindingRequest{GrantId: id.String()})
			if e != nil {
				return nil, status.Error(codes.FailedPrecondition, "legacy run binding unconfirmed")
			}
			org, e1 := uuid.Parse(receipt.GetOrgId())
			run, e2 := uuid.Parse(receipt.GetRunId())
			agent, e3 := uuid.Parse(receipt.GetAgentId())
			if e1 != nil || e2 != nil || e3 != nil || org != scope.OrgID || receipt.GetGrantId() != id.String() {
				return nil, status.Error(codes.PermissionDenied, "legacy binding mismatch")
			}
			binding = capability.LegacyBinding{GrantID: id, OrgID: org, RunID: run, AgentID: agent, Branch: receipt.GetBranch()}
		} else if e != nil {
			return nil, status.Error(codes.Unavailable, "legacy cancellation unavailable")
		}
		if e = s.grants.CancelVerifiedLegacyGrant(ctx, binding); e != nil {
			return nil, intentError(e)
		}
		return &identityv1.RevokeGrantResponse{Ok: true}, nil
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "grant receipt unavailable")
	}
	if err = s.grants.CancelVerifiedRunIntent(ctx, intent); err != nil {
		return nil, intentError(err)
	}
	return &identityv1.RevokeGrantResponse{Ok: true}, nil
}
