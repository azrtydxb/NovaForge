package work

import (
	"context"
	"strings"

	"github.com/google/uuid"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/gatenames"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func humanEditor(ctx context.Context) (authz.Scope, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || scope.ActorID == uuid.Nil || scope.ActorKind != "user" {
		return authz.Scope{}, status.Error(codes.PermissionDenied, "a scoped person must edit work")
	}
	return scope, nil
}

// PatchItem edits only unstarted human intent. All predicates and the old
// values are checked by one UPDATE, not by a racy read followed by a write.
func (g *GRPCServer) PatchItem(ctx context.Context, req *workv1.PatchItemRequest) (*workv1.PatchItemResponse, error) {
	scope, err := humanEditor(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID("id", req.GetId())
	if err != nil {
		return nil, err
	}
	repo, err := parseUUID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	if req.GetValues() == nil || req.GetExpected() == nil || len(req.GetUpdateMask().GetPaths()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "values, expected and update_mask are required")
	}
	fields := map[string]bool{}
	for _, field := range req.GetUpdateMask().GetPaths() {
		switch field {
		case "type", "goal", "acceptance", "constraints", "required_gates":
			fields[field] = true
		default:
			return nil, status.Errorf(codes.InvalidArgument, "field %q is not editable", field)
		}
	}
	v, old := req.GetValues(), req.GetExpected()
	if fields["type"] && !validTypes[v.GetType()] {
		return nil, status.Error(codes.InvalidArgument, "invalid type")
	}
	if fields["goal"] && strings.TrimSpace(v.GetGoal()) == "" {
		return nil, status.Error(codes.InvalidArgument, "goal is required")
	}
	for _, gate := range v.GetRequiredGates() {
		if !gatenames.Known(gate) {
			return nil, status.Errorf(codes.InvalidArgument, "unknown required gate %q", gate)
		}
	}
	tag, err := g.Store.pool.Exec(ctx, `UPDATE work.work_items w SET
 type=CASE WHEN $4 THEN $5 ELSE type END,
 goal=CASE WHEN $6 THEN $7 ELSE goal END,
 acceptance=CASE WHEN $8 THEN $9::text[] ELSE acceptance END,
 constraints=CASE WHEN $10 THEN $11::text[] ELSE constraints END,
 required_gates=CASE WHEN $12 THEN $13::text[] ELSE required_gates END
 WHERE w.org_id=$1 AND w.repo_id=$2 AND w.id=$3 AND w.state='open' AND NOT w.execution_claimed
 AND NOT EXISTS(SELECT 1 FROM work.maintenance_proposals p WHERE p.org_id=w.org_id AND p.work_item_id=w.id)
 AND w.type=$14 AND w.goal=$15 AND w.acceptance=$16::text[] AND w.constraints=$17::text[] AND w.required_gates=$18::text[]`,
		scope.OrgID, repo, id, fields["type"], v.GetType(), fields["goal"], v.GetGoal(), fields["acceptance"], nonNil(v.GetAcceptance()), fields["constraints"], nonNil(v.GetConstraints()), fields["required_gates"], nonNil(v.GetRequiredGates()), old.GetType(), old.GetGoal(), nonNil(old.GetAcceptance()), nonNil(old.GetConstraints()), nonNil(old.GetRequiredGates()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "patch work: %v", err)
	}
	if tag.RowsAffected() != 1 {
		return nil, status.Error(codes.Aborted, "work changed, is not open, has execution history, is a maintenance proposal, or is outside this repository; reload before editing")
	}
	item, err := g.GetItem(ctx, &workv1.GetItemRequest{Id: id.String()})
	if err != nil {
		return nil, err
	}
	return &workv1.PatchItemResponse{Item: item.GetItem()}, nil
}

// TransitionItem exposes only the user-approved open/blocked toggle. Execution,
// review and completion belong to their evidence-producing controllers.
func (g *GRPCServer) TransitionItem(ctx context.Context, req *workv1.TransitionItemRequest) (*workv1.TransitionItemResponse, error) {
	scope, err := humanEditor(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID("id", req.GetId())
	if err != nil {
		return nil, err
	}
	repo, err := parseUUID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	from, to := req.GetExpectedState(), req.GetToState()
	if !((from == "open" && to == "blocked") || (from == "blocked" && to == "open")) {
		return nil, status.Error(codes.InvalidArgument, "people may only move open work to blocked or blocked work to open")
	}
	tag, err := g.Store.pool.Exec(ctx, `UPDATE work.work_items w SET state=$4
 WHERE w.org_id=$1 AND w.repo_id=$2 AND w.id=$3 AND w.state=$5 AND NOT w.execution_claimed AND w.assignee_kind IS DISTINCT FROM 'agent'
 AND NOT EXISTS(SELECT 1 FROM work.maintenance_proposals p WHERE p.org_id=w.org_id AND p.work_item_id=w.id)`, scope.OrgID, repo, id, to, from)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "transition work: %v", err)
	}
	if tag.RowsAffected() != 1 {
		return nil, status.Error(codes.Aborted, "work changed, has execution history, is agent-assigned, is a maintenance proposal, or is outside this repository; reload before transitioning")
	}
	item, err := g.GetItem(ctx, &workv1.GetItemRequest{Id: id.String()})
	if err != nil {
		return nil, err
	}
	return &workv1.TransitionItemResponse{Item: item.GetItem()}, nil
}
func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (s *Store) isMaintenanceProposal(ctx context.Context, id uuid.UUID) (bool, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return false, err
	}
	var found bool
	err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM work.maintenance_proposals WHERE org_id=$1 AND work_item_id=$2)`, scope.OrgID, id).Scan(&found)
	return found, err
}
