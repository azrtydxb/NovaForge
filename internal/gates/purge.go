package gates

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
)

// Purger removes the gates service's rows for deleted runs and deleted
// organizations. The service owns three schemas — gates, approvals and
// secrets — and all three key rows on run ids it cannot trace to a
// repository, so a repository's deletion reaches them as the ids of the runs
// deleted with it.
type Purger struct {
	Pool *pgxpool.Pool
}

func purgeOrg(ctx context.Context) (uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return uuid.Nil, errors.New("purge requires an organization scope")
	}
	return scope.OrgID, nil
}

// PurgeRuns deletes gate evaluations, approval requests and credential leases
// of the given runs in the caller's organization. The organization is part of
// every delete, so a run id from another organization removes nothing.
func (p *Purger) PurgeRuns(ctx context.Context, runIDs []uuid.UUID) error {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return err
	}
	if len(runIDs) == 0 {
		return nil
	}
	for _, q := range []string{
		`DELETE FROM gates.gate_evaluations WHERE org_id = $1 AND run_id = ANY($2)`,
		`DELETE FROM approvals.approval_requests WHERE org_id = $1 AND run_id = ANY($2)`,
		`DELETE FROM secrets.secret_leases WHERE org_id = $1 AND run_id = ANY($2)`,
	} {
		if _, err := p.Pool.Exec(ctx, q, orgID, runIDs); err != nil {
			return fmt.Errorf("purge run rows: %w", err)
		}
	}
	return nil
}

// PurgeOrganization deletes every row of the caller's organization in the
// gates, approvals and secrets schemas, secret values included.
func (p *Purger) PurgeOrganization(ctx context.Context) error {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM gates.gate_evaluations WHERE org_id = $1`,
		`DELETE FROM approvals.approval_requests WHERE org_id = $1`,
		`DELETE FROM secrets.secret_leases WHERE org_id = $1`,
		`DELETE FROM secrets.secret_values WHERE org_id = $1`,
	} {
		if _, err := p.Pool.Exec(ctx, q, orgID); err != nil {
			return fmt.Errorf("purge organization: %w", err)
		}
	}
	return nil
}
