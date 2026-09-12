package work

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
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
	id, err := parseUUID("work_item_id", req.GetWorkItemId())
	if err != nil {
		return nil, err
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
		out = append(out, &workv1.MaintenanceProposal{
			Fingerprint:  p.Fingerprint,
			WorkItemKey:  p.WorkItemKey,
			WorkItemGoal: p.WorkItemGoal,
			WorkItemType: p.WorkItemType,
			State:        p.State,
			Resolved:     p.Resolved,
		})
	}
	return &workv1.ListMaintenanceProposalsResponse{Proposals: out}, nil
}
