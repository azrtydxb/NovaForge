package deployment

import (
	"context"
	"sort"

	"github.com/google/uuid"
	deploymentv1 "github.com/novaforge/novaforge/gen/novaforge/deployment/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

func (s *GRPCServer) ListTargets(ctx context.Context, r *deploymentv1.ListTargetsRequest) (*deploymentv1.ListTargetsResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	repo, err := deploymentID(r.GetRepoId())
	if err != nil {
		return nil, err
	}
	scope, _ := authz.FromContext(ctx)
	if err = authz.RequireOrg(ctx, scope.OrgID); err != nil {
		return nil, deploymentError(err)
	}
	out := &deploymentv1.ListTargetsResponse{}
	for _, target := range s.service.targets {
		if target.OrgID == scope.OrgID && target.RepoID == repo {
			out.Targets = append(out.Targets, &deploymentv1.Target{Name: target.Name, Environment: target.Environment, Revision: target.Revision})
		}
	}
	sort.Slice(out.Targets, func(i, j int) bool { return out.Targets[i].Name < out.Targets[j].Name })
	return out, nil
}
func (s *GRPCServer) ListDeployments(ctx context.Context, r *deploymentv1.ListDeploymentsRequest) (*deploymentv1.ListDeploymentsResponse, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	repo, err := deploymentID(r.GetRepoId())
	if err != nil {
		return nil, err
	}
	scope, _ := authz.FromContext(ctx)
	if err = authz.RequireOrg(ctx, scope.OrgID); err != nil {
		return nil, deploymentError(err)
	}
	rows, err := s.service.pool.Query(ctx, `SELECT id FROM deployment.operations WHERE org_id=$1 AND repo_id=$2 ORDER BY created_at DESC,id LIMIT 100`, scope.OrgID, repo)
	if err != nil {
		return nil, deploymentError(err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, deploymentError(err)
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, deploymentError(err)
	}
	out := &deploymentv1.ListDeploymentsResponse{}
	for _, id := range ids {
		op, err := s.service.Get(ctx, id)
		if err != nil {
			return nil, deploymentError(err)
		}
		out.Operations = append(out.Operations, operationProto(op))
	}
	return out, nil
}
