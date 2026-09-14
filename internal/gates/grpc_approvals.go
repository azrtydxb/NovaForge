package gates

import (
	"context"
	"errors"
	"log"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	"github.com/novaforge/novaforge/internal/approvals"
	"github.com/novaforge/novaforge/internal/authz"
)

// RequestApproval records a new pending approval request within the
// caller's organization. A request raised this way carries no head, so it
// never satisfies a merge: the approvals a merge needs are raised by the
// controller from the change itself (see Controller.approvalReasons).
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
		Id:         r.ID.String(),
		OrgId:      r.OrgID.String(),
		RunId:      r.RunID.String(),
		Action:     string(r.Action),
		Decision:   r.Decision,
		CreatedAt:  r.CreatedAt.Format(rfc3339),
		HeadSha:    r.HeadSHA,
		Reason:     r.Reason,
		Paths:      r.Paths,
		Comment:    r.Comment,
		AuthorKind: r.AuthorKind,
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
	if r.AuthorID != uuid.Nil {
		out.AuthorId = r.AuthorID.String()
	}
	return out
}

// ListApprovals lists a run's approval requests in every state, or the
// organization's pending requests when no run is named.
func (g *GRPCServer) ListApprovals(ctx context.Context, req *gatesv1.ListApprovalsRequest) (*gatesv1.ListApprovalsResponse, error) {
	if _, err := callerOrg(ctx); err != nil {
		return nil, err
	}
	var (
		found []approvals.ApprovalRequest
		err   error
	)
	if req.GetRunId() != "" {
		runID, perr := parseUUID("run_id", req.GetRunId())
		if perr != nil {
			return nil, perr
		}
		found, err = g.Approvals.ForRun(ctx, runID)
	} else {
		found, err = g.Approvals.Pending(ctx)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list approvals: %v", err)
	}
	out := make([]*gatesv1.ApprovalRequestMsg, 0, len(found))
	for _, r := range found {
		out = append(out, toProtoApprovalRequest(r))
	}
	return &gatesv1.ListApprovalsResponse{Requests: out}, nil
}

// ResolveApproval records the caller's decision on a pending approval request.
//
// Who may decide is the whole of this method. The decider is the caller —
// decided_by used to be read from the request, so anyone could record a
// decision under anyone's name. The caller must be a person (an agent, or a
// service acting for one, never approves anything), an owner or admin of the
// organization, and not the author of the change: a change approved by the
// one who made it has not been approved.
func (g *GRPCServer) ResolveApproval(ctx context.Context, req *gatesv1.ResolveApprovalRequest) (*gatesv1.ResolveApprovalResponse, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "no authorization scope for this call")
	}
	id, err := parseUUID("id", req.GetId())
	if err != nil {
		return nil, err
	}
	if req.GetDecision() != approvals.StateApproved && req.GetDecision() != approvals.StateDenied {
		return nil, status.Errorf(codes.InvalidArgument, "invalid decision %q", req.GetDecision())
	}
	if scope.ActorKind != "user" || scope.ActorID == uuid.Nil {
		return nil, status.Error(codes.PermissionDenied, "an approval is decided by a person")
	}
	if !scope.IsOrgAdmin() {
		return nil, status.Error(codes.PermissionDenied, "only an owner or admin of the organization may decide an approval")
	}

	current, err := g.Approvals.Get(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "no approval %s in this organization", id)
	}
	if current.AuthorID != uuid.Nil && current.AuthorID == scope.ActorID {
		return nil, status.Error(codes.PermissionDenied, "the author of a change cannot decide its approval")
	}

	decided, err := g.Approvals.Resolve(ctx, id, scope.ActorID, req.GetDecision(), req.GetComment())
	if errors.Is(err, approvals.ErrNotPending) {
		return nil, status.Errorf(codes.NotFound, "no pending approval %s in this organization", id)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve approval: %v", err)
	}

	// The decision is the run's proof as soon as it is made. If this write
	// fails, the next evaluation of the run writes it again, so the decision
	// itself — already committed — is not undone for a display write.
	if g.Controller != nil && decided.HeadSHA != "" {
		if err := g.Controller.recordApprovalProof(ctx, decided); err != nil {
			log.Printf("gates: record proof for approval %s: %v", id, err)
		}
	}
	return &gatesv1.ResolveApprovalResponse{Ok: true, Request: toProtoApprovalRequest(decided)}, nil
}
