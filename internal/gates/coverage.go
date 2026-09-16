package gates

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CoverageHistory retains the latest unavailable result too: skipping a failed
// evaluation would turn stale measurements into an apparently current check.
// Cached evaluations at the same head do not manufacture new history entries.
func (s *Store) CoverageHistory(ctx context.Context, repoID uuid.UUID) ([]Evaluation, error) {
	sc, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	if repoID == uuid.Nil {
		return nil, fmt.Errorf("repository id required")
	}
	rows, err := s.pool.Query(ctx, `SELECT id, org_id, run_id, gate, status, detail, target_sha, evaluated_at, repo_id, coverage_percent
 FROM gates.gate_evaluations WHERE org_id=$1 AND repo_id=$2 AND gate='tests'
 ORDER BY evaluated_at DESC, id DESC LIMIT 2`, sc.OrgID, repoID)
	if err != nil {
		return nil, fmt.Errorf("read coverage history: %w", err)
	}
	defer rows.Close()
	var result []Evaluation
	for rows.Next() {
		var e Evaluation
		if err := rows.Scan(&e.ID, &e.OrgID, &e.RunID, &e.Gate, &e.Status, &e.Detail, &e.TargetSHA, &e.EvaluatedAt, &e.RepoID, &e.CoveragePercent); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (g *GRPCServer) ListCoverageHistory(ctx context.Context, req *gatesv1.ListCoverageHistoryRequest) (*gatesv1.ListCoverageHistoryResponse, error) {
	if _, err := callerOrg(ctx); err != nil {
		return nil, err
	}
	repo, err := parseUUID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	if repo == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "repository id required")
	}
	evals, err := g.Controller.Store.CoverageHistory(ctx, repo)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read coverage history: %v", err)
	}
	out := &gatesv1.ListCoverageHistoryResponse{}
	for _, e := range evals {
		out.Evaluations = append(out.Evaluations, toProtoEvaluation(e))
	}
	return out, nil
}
