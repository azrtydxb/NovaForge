package graph

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/knowledge"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SupersedeKnowledge is a human owner/admin correction, not authority conferred
// by graph's service identity. Preserve the verified scope all the way to storage.
func (s *GRPCServer) SupersedeKnowledge(ctx context.Context, req *graphv1.SupersedeKnowledgeRequest) (*graphv1.SupersedeKnowledgeResponse, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || !scope.IsOrgAdmin() {
		return nil, status.Error(codes.PermissionDenied, "organization owner or admin required")
	}
	oldID, err := uuid.Parse(req.GetOldId())
	if err != nil || oldID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid old_id")
	}
	newID, err := uuid.Parse(req.GetReplacementId())
	if err != nil || newID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "invalid replacement_id")
	}
	if s.Knowledge == nil {
		return nil, status.Error(codes.Unavailable, "knowledge store unavailable")
	}
	if err = s.Knowledge.Supersede(ctx, oldID, newID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "knowledge entry not found")
		}
		if errors.Is(err, knowledge.ErrSupersessionConflict) {
			return nil, status.Error(codes.FailedPrecondition, "invalid or conflicting supersession")
		}
		return nil, status.Error(codes.Internal, "knowledge supersession failed")
	}
	return &graphv1.SupersedeKnowledgeResponse{}, nil
}
