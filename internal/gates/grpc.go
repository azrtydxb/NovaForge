package gates

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	"github.com/novaforge/novaforge/internal/approvals"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/secrets"
)

const rfc3339 = "2006-01-02T15:04:05.999999999Z07:00"

// GRPCServer implements gatesv1.GatesServiceServer: gate evaluation, merge
// eligibility, human approvals, and short-lived secret leases. Every method
// derives the caller's organization from authz.FromContext — never from a
// field of the request message — so a client cannot simply name a
// different org.id and be believed.
type GRPCServer struct {
	gatesv1.UnimplementedGatesServiceServer

	Controller *Controller
	Approvals  *approvals.Store
	Secrets    *secrets.Broker
	Grants     *capability.Store
}

// NewGRPCServer wraps the given dependencies as a gatesv1.GatesServiceServer.
func NewGRPCServer(controller *Controller, approvalsStore *approvals.Store, secretsBroker *secrets.Broker, grants *capability.Store) *GRPCServer {
	return &GRPCServer{Controller: controller, Approvals: approvalsStore, Secrets: secretsBroker, Grants: grants}
}

func callerOrg(ctx context.Context) (uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return uuid.Nil, status.Error(codes.PermissionDenied, "no authorization scope for this call")
	}
	return scope.OrgID, nil
}

func parseUUID(field, raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, status.Errorf(codes.InvalidArgument, "invalid %s %q: %v", field, raw, err)
	}
	return id, nil
}

// statusFromErr preserves err's own gRPC status code (e.g. a RunLookup
// backed by the reviews service returning PermissionDenied for a run
// outside the caller's organization) rather than flattening every failure
// into def.
func statusFromErr(err error, def codes.Code, msg string) error {
	if s, ok := status.FromError(err); ok && s.Code() != codes.Unknown {
		return s.Err()
	}
	return status.Errorf(def, "%s: %v", msg, err)
}

func toProtoEvaluation(e Evaluation) *gatesv1.Evaluation {
	return &gatesv1.Evaluation{
		Id:          e.ID.String(),
		OrgId:       e.OrgID.String(),
		RunId:       e.RunID.String(),
		Gate:        e.Gate,
		Status:      e.Status,
		Detail:      e.Detail,
		TargetSha:   e.TargetSHA,
		EvaluatedAt: e.EvaluatedAt.Format(rfc3339),
	}
}

// Evaluate runs every gate resolved for the run whose latest evaluation is
// stale relative to its current head, within the caller's organization.
func (g *GRPCServer) Evaluate(ctx context.Context, req *gatesv1.EvaluateRequest) (*gatesv1.EvaluateResponse, error) {
	if _, err := callerOrg(ctx); err != nil {
		return nil, err
	}
	runID, err := parseUUID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	evals, err := g.Controller.Evaluate(ctx, runID)
	if err != nil {
		return nil, statusFromErr(err, codes.Internal, "evaluate gates")
	}
	out := make([]*gatesv1.Evaluation, len(evals))
	for i, e := range evals {
		out[i] = toProtoEvaluation(e)
	}
	return &gatesv1.EvaluateResponse{Evaluations: out}, nil
}

// MayMerge reports whether a run may merge: every gate required for its
// target has a passing evaluation at its current head SHA.
func (g *GRPCServer) MayMerge(ctx context.Context, req *gatesv1.MayMergeRequest) (*gatesv1.MayMergeResponse, error) {
	if _, err := callerOrg(ctx); err != nil {
		return nil, err
	}
	runID, err := parseUUID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	allowed, reasons, err := g.Controller.MayMerge(ctx, runID)
	if err != nil {
		return nil, statusFromErr(err, codes.Internal, "may merge")
	}
	return &gatesv1.MayMergeResponse{Allowed: allowed, Reasons: reasons}, nil
}

// ListEvaluations lists every evaluation recorded for a run, within the
// caller's organization.
func (g *GRPCServer) ListEvaluations(ctx context.Context, req *gatesv1.ListEvaluationsRequest) (*gatesv1.ListEvaluationsResponse, error) {
	if _, err := callerOrg(ctx); err != nil {
		return nil, err
	}
	runID, err := parseUUID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	evals, err := g.Controller.Store.ListEvaluations(ctx, runID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list evaluations: %v", err)
	}
	out := make([]*gatesv1.Evaluation, len(evals))
	for i, e := range evals {
		out[i] = toProtoEvaluation(e)
	}
	return &gatesv1.ListEvaluationsResponse{Evaluations: out}, nil
}

// RequestApproval records a new pending approval request within the
// caller's organization.
func (g *GRPCServer) RequestApproval(ctx context.Context, req *gatesv1.RequestApprovalRequest) (*gatesv1.RequestApprovalResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	runID, err := parseUUID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	created, err := g.Approvals.Request(ctx, approvals.ApprovalRequest{
		OrgID:  orgID,
		RunID:  runID,
		Action: approvals.Action(req.GetAction()),
		Detail: map[string]any{"raw": req.GetDetailJson()},
	})
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "request approval: %v", err)
	}
	return &gatesv1.RequestApprovalResponse{Request: toProtoApprovalRequest(created)}, nil
}

func toProtoApprovalRequest(r approvals.ApprovalRequest) *gatesv1.ApprovalRequestMsg {
	out := &gatesv1.ApprovalRequestMsg{
		Id:        r.ID.String(),
		OrgId:     r.OrgID.String(),
		RunId:     r.RunID.String(),
		Action:    string(r.Action),
		Decision:  r.Decision,
		CreatedAt: r.CreatedAt.Format(rfc3339),
	}
	if raw, ok := r.Detail["raw"].(string); ok {
		out.DetailJson = raw
	}
	if r.DecidedBy != uuid.Nil {
		out.DecidedBy = r.DecidedBy.String()
	}
	if !r.DecidedAt.IsZero() {
		out.DecidedAt = r.DecidedAt.Format(rfc3339)
	}
	return out
}

// ResolveApproval records a decision on a pending approval request. The
// request must currently be pending within the caller's organization: an
// approval id belonging to another organization, or already decided, is
// refused rather than silently applied.
func (g *GRPCServer) ResolveApproval(ctx context.Context, req *gatesv1.ResolveApprovalRequest) (*gatesv1.ResolveApprovalResponse, error) {
	if _, err := callerOrg(ctx); err != nil {
		return nil, err
	}
	id, err := parseUUID("id", req.GetId())
	if err != nil {
		return nil, err
	}
	decidedBy, err := parseUUID("decided_by", req.GetDecidedBy())
	if err != nil {
		return nil, err
	}
	if req.GetDecision() != "approved" && req.GetDecision() != "denied" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid decision %q", req.GetDecision())
	}

	pending, err := g.Approvals.Pending(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list pending approvals: %v", err)
	}
	found := false
	for _, p := range pending {
		if p.ID == id {
			found = true
			break
		}
	}
	if !found {
		return nil, status.Errorf(codes.NotFound, "no pending approval %s in this organization", id)
	}

	if err := g.Approvals.Resolve(ctx, id, decidedBy, req.GetDecision()); err != nil {
		return nil, status.Errorf(codes.Internal, "resolve approval: %v", err)
	}
	return &gatesv1.ResolveApprovalResponse{Ok: true}, nil
}

// IssueLease issues a short-lived, single-use secret lease for a run, under
// the given capability grant. The grant must belong to the caller's
// organization.
func (g *GRPCServer) IssueLease(ctx context.Context, req *gatesv1.IssueLeaseRequest) (*gatesv1.IssueLeaseResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	runID, err := parseUUID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	grantID, err := parseUUID("grant_id", req.GetGrantId())
	if err != nil {
		return nil, err
	}
	grant, err := g.Grants.Resolve(ctx, grantID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "resolve grant: %v", err)
	}
	if grant.OrgID != orgID {
		return nil, status.Error(codes.PermissionDenied, "grant does not belong to this organization")
	}
	ttl := time.Duration(req.GetTtlSeconds()) * time.Second
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	lease, err := g.Secrets.Issue(ctx, runID, grant, req.GetName(), ttl)
	if err != nil {
		return nil, status.Errorf(codes.PermissionDenied, "issue lease: %v", err)
	}
	return &gatesv1.IssueLeaseResponse{
		LeaseId:   lease.ID.String(),
		Token:     lease.Token,
		Name:      lease.Name,
		ExpiresAt: lease.ExpiresAt.Format(rfc3339),
	}, nil
}

// RedeemLease consumes a lease token once, returning the decrypted secret
// value. The lease must have been issued to the run named in the request.
func (g *GRPCServer) RedeemLease(ctx context.Context, req *gatesv1.RedeemLeaseRequest) (*gatesv1.RedeemLeaseResponse, error) {
	if _, err := callerOrg(ctx); err != nil {
		return nil, err
	}
	runID, err := parseUUID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	scoped := secrets.WithRunID(ctx, runID)
	value, err := g.Secrets.Redeem(scoped, req.GetToken())
	if err != nil {
		return nil, status.Errorf(codes.PermissionDenied, "redeem lease: %v", err)
	}
	return &gatesv1.RedeemLeaseResponse{Value: value}, nil
}
