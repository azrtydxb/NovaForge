package reviews

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// GetExceptions returns what needs a person's attention in the caller's
// organization: the counts first, then the runs behind them.
//
// The point of the platform is that humans review exceptions rather than every
// generated line, so this is the query the dashboard is built from.
func (s *GRPCServer) GetExceptions(ctx context.Context, _ *reviewsv1.GetExceptionsRequest) (*reviewsv1.GetExceptionsResponse, error) {
	sc, err := authz.FromContext(ctx)
	if err != nil || sc.OrgID == uuid.Nil {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}
	summary, items, err := s.Store.Exceptions(ctx, sc.OrgID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "exceptions: %v", err)
	}
	out := make([]*reviewsv1.ExceptionItem, 0, len(items))
	for _, it := range items {
		out = append(out, &reviewsv1.ExceptionItem{
			Key: it.Key, Title: it.Title, State: it.State, Reason: it.Reason,
		})
	}
	return &reviewsv1.GetExceptionsResponse{
		Summary: &reviewsv1.ExceptionSummary{
			AgentsRunning:         int32(summary.AgentsRunning),
			ReadyToAutoMerge:      int32(summary.ReadyToAutoMerge),
			NeedHumanReview:       int32(summary.NeedHumanReview),
			ArchitectureDecisions: int32(summary.ArchitectureDecisions),
			GateFailures:          int32(summary.GateFailures),
			AgentsBlocked:         int32(summary.AgentsBlocked),
		},
		Items: out,
	}, nil
}
