package approvals

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
)

// EnsureDeployment records one immutable deployment approval, or reads the
// identical prior request after a retry. Its id is the deployment operation id;
// free-form RequestApproval requests can never stand in for this binding.
func (s *Store) EnsureDeployment(ctx context.Context, id, runID uuid.UUID, action Action, detail string) (ApprovalRequest, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return ApprovalRequest{}, err
	}
	if scope.OrgID == uuid.Nil || scope.ActorID == uuid.Nil || id == uuid.Nil || runID == uuid.Nil || detail == "" || (action != ActionDeployStaging && action != ActionDeployProduction) {
		return ApprovalRequest{}, errors.New("invalid deployment approval binding")
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO approvals.approval_requests
 (id, org_id, run_id, action, detail, decision, author_id, author_kind, reason)
 VALUES ($1,$2,$3,$4,$5,'pending',$6,$7,'Deploy the exact target and artifact in detail') ON CONFLICT (id) DO NOTHING`,
		id, scope.OrgID, runID, action, map[string]any{"raw": detail}, scope.ActorID, scope.ActorKind)
	if err != nil {
		return ApprovalRequest{}, err
	}
	req, err := s.Get(ctx, id)
	if err != nil {
		return ApprovalRequest{}, err
	}
	if req.RunID != runID || req.Action != action || req.AuthorID != scope.ActorID || req.AuthorKind != scope.ActorKind || req.Detail["raw"] != detail || req.HeadSHA != "" {
		return ApprovalRequest{}, errors.New("deployment approval binding conflict")
	}
	return req, nil
}
