package work

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// CompletedSince counts the organization's Work Items that are done and reached
// done at or after since.
//
// It counts what is done now, not what was ever marked done: done_at is cleared
// when an item leaves done (see the 000009 migration), so a completed item that
// was reopened is not counted, and one completed twice counts once, for its
// latest completion. The predicate on state is redundant with done_at being set
// and is kept so the intent survives a future change to how done_at is
// maintained.
func (s *Store) CompletedSince(ctx context.Context, orgID uuid.UUID, since time.Time) (int, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return 0, err
	}
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM work.work_items
		WHERE org_id = $1 AND state = 'done' AND done_at IS NOT NULL AND done_at >= $2`,
		orgID, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count completed work: %w", err)
	}
	return n, nil
}

// CountCompleted reports how much of the caller's organization's work finished
// since the given instant. The caller chooses the instant so a product surface can
// ask for its own day; this service does not assume a timezone on its behalf.
func (g *GRPCServer) CountCompleted(ctx context.Context, req *workv1.CountCompletedRequest) (*workv1.CountCompletedResponse, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return nil, status.Error(codes.PermissionDenied, "no organization scope for this call")
	}
	since, err := time.Parse(time.RFC3339, req.GetSince())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "since must be an RFC 3339 time")
	}
	n, err := g.Store.CompletedSince(ctx, scope.OrgID, since)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "count completed work: %v", err)
	}
	return &workv1.CountCompletedResponse{Completed: int64(n)}, nil
}
