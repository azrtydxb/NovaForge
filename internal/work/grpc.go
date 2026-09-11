package work

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// GRPCServer implements workv1.WorkServiceServer. Every method derives the
// caller's organization from authz.FromContext and passes it as an explicit
// predicate to every query — never from a field of the request message, so
// a client cannot simply name a different org and be believed.
type GRPCServer struct {
	workv1.UnimplementedWorkServiceServer

	Store *Store
}

// NewGRPCServer wraps store as a workv1.WorkServiceServer.
func NewGRPCServer(store *Store) *GRPCServer {
	return &GRPCServer{Store: store}
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

func toProtoItem(it Item) *workv1.WorkItem {
	item := &workv1.WorkItem{
		Id:            it.ID.String(),
		OrgId:         it.OrgID.String(),
		RepoId:        it.RepoID.String(),
		Key:           it.Key,
		Type:          it.Type,
		Goal:          it.Goal,
		Acceptance:    it.Acceptance,
		Constraints:   it.Constraints,
		RequiredGates: it.RequiredGates,
		AssigneeKind:  it.AssigneeKind,
		State:         it.State,
		CreatedAt:     it.CreatedAt.Format(rfc3339),
	}
	if it.AssigneeID != uuid.Nil {
		item.AssigneeId = it.AssigneeID.String()
	}
	return item
}

const rfc3339 = "2006-01-02T15:04:05.999999999Z07:00"

// CreateItem creates a Work Item within the caller's organization.
func (g *GRPCServer) CreateItem(ctx context.Context, req *workv1.CreateItemRequest) (*workv1.CreateItemResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	repoID, err := parseUUID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	item, err := g.Store.Create(ctx, Item{
		OrgID:         orgID,
		RepoID:        repoID,
		Type:          req.GetType(),
		Goal:          req.GetGoal(),
		Acceptance:    req.GetAcceptance(),
		Constraints:   req.GetConstraints(),
		RequiredGates: req.GetRequiredGates(),
	})
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "create work item: %v", err)
	}
	return &workv1.CreateItemResponse{Item: toProtoItem(item)}, nil
}

// GetItem looks up a Work Item by id or by its human-readable key.
// Exactly one of Id or Key must be set.
func (g *GRPCServer) GetItem(ctx context.Context, req *workv1.GetItemRequest) (*workv1.GetItemResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	var item Item
	switch {
	case req.GetId() != "":
		id, perr := parseUUID("id", req.GetId())
		if perr != nil {
			return nil, perr
		}
		item, err = g.Store.get(ctx, orgID, id)
	case req.GetKey() != "":
		item, err = g.Store.GetByKey(ctx, orgID, req.GetKey())
	default:
		return nil, status.Error(codes.InvalidArgument, "one of id or key is required")
	}
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "get work item: %v", err)
	}
	if item.OrgID != orgID {
		return nil, status.Error(codes.PermissionDenied, "work item does not belong to this organization")
	}
	return &workv1.GetItemResponse{Item: toProtoItem(item)}, nil
}

// ListItems lists Work Items for a repository within the caller's
// organization, optionally filtered by state.
func (g *GRPCServer) ListItems(ctx context.Context, req *workv1.ListItemsRequest) (*workv1.ListItemsResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	repoID, err := parseUUID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	items, err := g.Store.List(ctx, orgID, repoID, req.GetState())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list work items: %v", err)
	}
	out := make([]*workv1.WorkItem, len(items))
	for i, it := range items {
		out[i] = toProtoItem(it)
	}
	return &workv1.ListItemsResponse{Items: out}, nil
}

// AssignItem sets the assignee of a Work Item within the caller's
// organization.
func (g *GRPCServer) AssignItem(ctx context.Context, req *workv1.AssignItemRequest) (*workv1.AssignItemResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID("id", req.GetId())
	if err != nil {
		return nil, err
	}
	assigneeID, err := parseUUID("assignee_id", req.GetAssigneeId())
	if err != nil {
		return nil, err
	}
	if err := g.Store.Assign(ctx, id, assigneeID, req.GetAssigneeKind()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "assign work item: %v", err)
	}
	item, err := g.Store.get(ctx, orgID, id)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "get assigned work item: %v", err)
	}
	return &workv1.AssignItemResponse{Item: toProtoItem(item)}, nil
}
