package agents

import (
	"context"
	"github.com/google/uuid"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetGrantIntent returns a persisted receipt only to the grant owner. It is
// available during cancellation as well: terminal state must not prevent fencing.
func (g *GRPCServer) GetGrantIntent(ctx context.Context, req *agentsv1.GetGrantIntentRequest) (*agentsv1.GetGrantIntentResponse, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || scope.ActorKind != "service" || scope.ServiceName != "identity" {
		return nil, status.Error(codes.PermissionDenied, "identity service required")
	}
	id, err := parseUUID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	run, err := g.Store.GetRun(ctx, id)
	if err != nil {
		return nil, status.Error(codes.NotFound, "run receipt unavailable")
	}
	if run.GrantIntent == nil || run.GrantIntent.Grant.OrgID != scope.OrgID || run.GrantIntent.Grant.SubjectID != run.AgentID || run.GrantIntent.IssuerID != run.SponsorID || run.GrantIntent.RunID != run.ID {
		return nil, status.Error(codes.FailedPrecondition, "run receipt inconsistent")
	}
	if _, err = g.Store.GetAgent(ctx, run.AgentID); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "run subject unavailable")
	}
	return &agentsv1.GetGrantIntentResponse{Intent: capability.IntentProto(*run.GrantIntent)}, nil
}

// GetLegacyGrantBinding exposes only an unambiguous persisted binding, not
// reconstructed permissions, issuer membership, or an invented issuance intent.
func (g *GRPCServer) GetLegacyGrantBinding(ctx context.Context, req *agentsv1.GetLegacyGrantBindingRequest) (*agentsv1.GetLegacyGrantBindingResponse, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || scope.ActorKind != "service" || scope.ServiceName != "identity" {
		return nil, status.Error(codes.PermissionDenied, "identity service required")
	}
	grant, err := parseUUID("grant_id", req.GetGrantId())
	if err != nil {
		return nil, err
	}
	rows, err := g.Store.pool.Query(ctx, `SELECT id,agent_id,branch,grant_issue_intent IS NULL FROM agents.agent_runs WHERE org_id=$1 AND grant_id=$2 LIMIT 2`, scope.OrgID, grant)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "legacy binding unavailable")
	}
	defer rows.Close()
	var result *agentsv1.GetLegacyGrantBindingResponse
	for rows.Next() {
		var run, agent uuid.UUID
		var branch string
		var legacy bool
		if err = rows.Scan(&run, &agent, &branch, &legacy); err != nil {
			return nil, status.Error(codes.Unavailable, "legacy binding unavailable")
		}
		if result != nil || !legacy || agent == uuid.Nil || branch == "" {
			return nil, status.Error(codes.FailedPrecondition, "legacy binding ambiguous")
		}
		result = &agentsv1.GetLegacyGrantBindingResponse{OrgId: scope.OrgID.String(), RunId: run.String(), AgentId: agent.String(), Branch: branch, GrantId: grant.String()}
	}
	if rows.Err() != nil {
		return nil, status.Error(codes.Unavailable, "legacy binding unavailable")
	}
	if result == nil {
		return nil, status.Error(codes.FailedPrecondition, "legacy binding missing")
	}
	return result, nil
}
