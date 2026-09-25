package gates

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/secrets"
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

// PurgeRuns fences sandbox and credential admission before deleting evaluations
// and approvals. Unresolved obligations keep the deletion event retryable. Every
// delete includes the caller's organization, so a foreign run removes nothing.
func (p *Purger) PurgeRuns(ctx context.Context, runIDs []uuid.UUID) error {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return err
	}
	if len(runIDs) == 0 {
		return nil
	}
	sandboxErr := p.fenceSandbox(ctx, runIDs)
	// Fence both owners even when either has pending cleanup: a live sandbox
	// must not leave credential issuance open while its deletion is retried.
	credentialErr := secrets.PurgeCredentials(ctx, p.Pool, runIDs)
	if err := errors.Join(sandboxErr, credentialErr); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM gates.gate_evaluations WHERE org_id = $1 AND run_id = ANY($2)`,
		`DELETE FROM approvals.approval_requests WHERE org_id = $1 AND run_id = ANY($2)`,
	} {
		if _, err := p.Pool.Exec(ctx, q, orgID, runIDs); err != nil {
			return fmt.Errorf("purge run rows: %w", err)
		}
	}
	return nil
}

// PurgeOrganization deletes every row of the caller's organization in the
// gates, approvals and secrets schemas, secret values included, only after
// sandbox and credential cleanup is confirmed. Admission tombstones and sandbox
// lifecycle evidence are retained, including records for retired targets.
func (p *Purger) PurgeOrganization(ctx context.Context) error {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return err
	}
	sandboxErr := p.fenceSandbox(ctx, nil)
	credentialErr := secrets.PurgeCredentials(ctx, p.Pool, nil)
	if err := errors.Join(sandboxErr, credentialErr); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM gates.gate_evaluations WHERE org_id = $1`,
		`DELETE FROM approvals.approval_requests WHERE org_id = $1`,
		`DELETE FROM secrets.secret_values WHERE org_id = $1`,
	} {
		if _, err := p.Pool.Exec(ctx, q, orgID); err != nil {
			return fmt.Errorf("purge organization: %w", err)
		}
	}
	return nil
}
