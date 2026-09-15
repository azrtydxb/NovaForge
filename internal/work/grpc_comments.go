package work

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// AddComment appends a comment to a Work Item's thread, attributed to the
// caller. Agents comment through the same RPC people do — an agent
// recording what it decided is part of the Work Item's history, not a
// separate channel.
func (g *GRPCServer) AddComment(ctx context.Context, req *workv1.AddCommentRequest) (*workv1.AddCommentResponse, error) {
	id, err := parseUUID("work_item_id", req.GetWorkItemId())
	if err != nil {
		return nil, err
	}
	if req.GetBody() == "" {
		return nil, status.Error(codes.InvalidArgument, "body is required")
	}
	c, err := g.Store.AddComment(ctx, id, req.GetBody())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "add comment: %v", err)
	}
	return &workv1.AddCommentResponse{Comment: toProtoComment(c)}, nil
}

// ListComments returns a Work Item's thread, oldest first.
func (g *GRPCServer) ListComments(ctx context.Context, req *workv1.ListCommentsRequest) (*workv1.ListCommentsResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID("work_item_id", req.GetWorkItemId())
	if err != nil {
		return nil, err
	}
	// An item outside the caller's organization is not found, rather than
	// found with an empty discussion: an empty answer to an id the caller does
	// not own says the id exists nowhere they can see only by accident.
	if _, err := g.Store.get(ctx, orgID, id); err != nil {
		return nil, status.Errorf(codes.NotFound, "work item %s not found", id)
	}
	comments, err := g.Store.ListComments(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list comments: %v", err)
	}
	out := make([]*workv1.Comment, 0, len(comments))
	for _, c := range comments {
		out = append(out, toProtoComment(c))
	}
	return &workv1.ListCommentsResponse{Comments: out}, nil
}

func toProtoComment(c Comment) *workv1.Comment {
	return &workv1.Comment{
		Id:         c.ID.String(),
		WorkItemId: c.WorkItemID.String(),
		AuthorId:   c.AuthorID.String(),
		AuthorKind: c.AuthorKind,
		Body:       c.Body,
		CreatedAt:  c.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// ListMaintenanceProposals returns what the scanners proposed for one
// repository. Nothing here executed a fix: a proposal is a plain, unassigned
// Work Item that a person decides about.
func (g *GRPCServer) ListMaintenanceProposals(ctx context.Context, req *workv1.ListMaintenanceProposalsRequest) (*workv1.ListMaintenanceProposalsResponse, error) {
	repoID, err := parseUUID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	found, err := g.Store.ListProposals(ctx, repoID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list maintenance proposals: %v", err)
	}
	out := make([]*workv1.MaintenanceProposal, 0, len(found))
	for _, p := range found {
		out = append(out, toProtoProposal(p))
	}
	return &workv1.ListMaintenanceProposalsResponse{Proposals: out}, nil
}

// ApproveMaintenanceProposal records a person's approval of a proposal and
// assigns its Work Item, which is what makes it actionable: until then
// agent-runtime refuses to start a run against it.
func (g *GRPCServer) ApproveMaintenanceProposal(ctx context.Context, req *workv1.ApproveMaintenanceProposalRequest) (*workv1.ApproveMaintenanceProposalResponse, error) {
	repoID, err := parseUUID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	var assignee uuid.UUID
	kind := req.GetAssigneeKind()
	if req.GetAssigneeId() != "" {
		if assignee, err = parseUUID("assignee_id", req.GetAssigneeId()); err != nil {
			return nil, err
		}
		if kind != "user" && kind != "agent" {
			return nil, status.Errorf(codes.InvalidArgument, "assignee_kind must be user or agent, got %q", kind)
		}
	}
	p, err := g.Store.ApproveProposal(ctx, repoID, req.GetFingerprint(), assignee, kind)
	if err != nil {
		return nil, proposalStatus("approve maintenance proposal", err)
	}
	return &workv1.ApproveMaintenanceProposalResponse{Proposal: toProtoProposal(p)}, nil
}

// DismissMaintenanceProposal records a person's dismissal of a proposal, with
// the reason, and closes its Work Item.
func (g *GRPCServer) DismissMaintenanceProposal(ctx context.Context, req *workv1.DismissMaintenanceProposalRequest) (*workv1.DismissMaintenanceProposalResponse, error) {
	repoID, err := parseUUID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetReason()) == "" {
		return nil, status.Error(codes.InvalidArgument, "a dismissal needs a reason")
	}
	p, err := g.Store.DismissProposal(ctx, repoID, req.GetFingerprint(), req.GetReason())
	if err != nil {
		return nil, proposalStatus("dismiss maintenance proposal", err)
	}
	return &workv1.DismissMaintenanceProposalResponse{Proposal: toProtoProposal(p)}, nil
}

// proposalStatus maps a decision's failure onto the status a caller can act on.
func proposalStatus(op string, err error) error {
	switch {
	case errors.Is(err, ErrNotAPerson), errors.Is(err, authz.ErrNoScope):
		return status.Errorf(codes.PermissionDenied, "%s: %v", op, err)
	case errors.Is(err, ErrProposalNotFound):
		return status.Errorf(codes.NotFound, "%s: %v", op, err)
	case errors.Is(err, ErrProposalDecided):
		return status.Errorf(codes.FailedPrecondition, "%s: %v", op, err)
	default:
		return status.Errorf(codes.Internal, "%s: %v", op, err)
	}
}

func toProtoProposal(p Proposal) *workv1.MaintenanceProposal {
	out := &workv1.MaintenanceProposal{
		Fingerprint:   p.Fingerprint,
		WorkItemKey:   p.WorkItemKey,
		WorkItemGoal:  p.WorkItemGoal,
		WorkItemType:  p.WorkItemType,
		State:         p.State,
		Resolved:      p.Resolved,
		Decision:      p.Decision,
		DismissReason: p.DismissReason,
		AssigneeKind:  p.AssigneeKind,
	}
	if p.DecidedBy != uuid.Nil {
		out.DecidedBy = p.DecidedBy.String()
	}
	if p.DecidedAt != nil {
		out.DecidedAt = p.DecidedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if p.AssigneeID != uuid.Nil {
		out.AssigneeId = p.AssigneeID.String()
	}
	return out
}
