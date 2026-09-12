package work

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// ListSubtasks returns an epic's children with their readiness.
//
// Readiness is taken from the same query the swarm scheduler uses rather than
// recomputed from the dependency list, so what a caller sees and what the
// scheduler acts on cannot disagree.
func (s *GRPCServer) ListSubtasks(ctx context.Context, req *workv1.ListSubtasksRequest) (*workv1.ListSubtasksResponse, error) {
	sc, err := authz.FromContext(ctx)
	if err != nil || sc.OrgID == uuid.Nil {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}
	epic, err := s.Store.GetByKey(ctx, sc.OrgID, req.GetEpicKey())
	if err != nil {
		return nil, status.Error(codes.NotFound, "no such work item")
	}
	children, err := s.Store.Children(ctx, sc.OrgID, epic.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list subtasks: %v", err)
	}
	ready, err := s.Store.Ready(ctx, sc.OrgID, epic.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve readiness: %v", err)
	}
	isReady := make(map[uuid.UUID]bool, len(ready))
	for _, r := range ready {
		isReady[r.ID] = true
	}

	out := make([]*workv1.Subtask, 0, len(children))
	for _, c := range children {
		out = append(out, &workv1.Subtask{Item: toProtoItem(c), Ready: isReady[c.ID]})
	}
	return &workv1.ListSubtasksResponse{Subtasks: out}, nil
}
