package work

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// Decomposer turns an epic into dependency-ordered subtasks and writes them.
// It is an interface here so this package does not depend on the swarm planner
// or, through it, on a model client.
type Decomposer interface {
	DecomposeAndMaterialise(ctx context.Context, epic Item) ([]Item, error)
}

// SetDecomposer wires epic decomposition. Left nil, DecomposeEpic reports that
// it is unavailable rather than pretending an epic has no subtasks.
func (s *GRPCServer) SetDecomposer(d Decomposer) { s.decomposer = d }

// DecomposeEpic breaks an epic into subtasks and records them as Work Items
// with dependency edges.
func (s *GRPCServer) DecomposeEpic(ctx context.Context, req *workv1.DecomposeEpicRequest) (*workv1.DecomposeEpicResponse, error) {
	sc, err := authz.FromContext(ctx)
	if err != nil || sc.OrgID == uuid.Nil {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}
	if s.decomposer == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"decomposition is unavailable: no model is configured for this deployment")
	}
	epic, err := s.Store.GetByKey(ctx, sc.OrgID, req.GetEpicKey())
	if err != nil {
		return nil, status.Error(codes.NotFound, "no such work item")
	}
	subs, err := s.decomposer.DecomposeAndMaterialise(ctx, epic)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "decompose %s: %v", epic.Key, err)
	}
	out := make([]*workv1.WorkItem, 0, len(subs))
	for _, it := range subs {
		out = append(out, toProtoItem(it))
	}
	return &workv1.DecomposeEpicResponse{Subtasks: out}, nil
}
