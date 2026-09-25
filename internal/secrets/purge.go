package secrets

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/authz"
)

// lockOrganization orders admission and deletion before run/attempt locks.
// Tombstones deliberately survive purge: delayed events must not reopen scope.
func lockOrganization(ctx context.Context, tx pgx.Tx, org uuid.UUID) (bool, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO secrets.credential_organizations(org_id) VALUES($1) ON CONFLICT DO NOTHING`, org); err != nil {
		return false, err
	}
	var closed bool
	err := tx.QueryRow(ctx, `SELECT closed FROM secrets.credential_organizations WHERE org_id=$1 FOR UPDATE`, org).Scan(&closed)
	return closed, err
}

// PurgeCredentials fences issuance and deletes only proven-complete leases.
// A nil runIDs means organization deletion; an empty non-nil slice is a no-op.
// Pending provider operations and legacy rotation-required records survive and
// return an error so deletion consumers cannot acknowledge false completion.
// Provider I/O belongs to RetryRevocations, never this transaction. Its caller
// must include deleted organizations in the authenticated retry schedule.
func PurgeCredentials(ctx context.Context, pool *pgxpool.Pool, runIDs []uuid.UUID) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	if err := requireOrg(ctx, scope.OrgID); err != nil {
		return err
	}
	if runIDs != nil && len(runIDs) == 0 {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = lockOrganization(ctx, tx, scope.OrgID); err != nil {
		return err
	}
	if runIDs == nil {
		_, err = tx.Exec(ctx, `UPDATE secrets.credential_organizations SET closed=true WHERE org_id=$1`, scope.OrgID)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO secrets.credential_scopes(org_id,run_id,closed) SELECT DISTINCT $1::uuid,unnest($2::uuid[]),true ON CONFLICT(org_id,run_id) DO UPDATE SET closed=true`, scope.OrgID, runIDs)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE secrets.secret_leases SET revocation_requested=true,provider_ready=false WHERE org_id=$1 AND ($2::uuid[] IS NULL OR run_id=ANY($2)) AND (revoked_at IS NULL OR (provider_endpoint='' AND redeemed_count>0))`, scope.OrgID, runIDs); err != nil {
		return err
	}
	// An unredeemed legacy delivery token has released no external material.
	// It needs only local deletion; a redeemed one still requires rotation.
	if _, err = tx.Exec(ctx, `DELETE FROM secrets.secret_leases WHERE org_id=$1 AND ($2::uuid[] IS NULL OR run_id=ANY($2)) AND ((revoked_at IS NOT NULL AND NOT (provider_endpoint='' AND redeemed_count>0)) OR (provider_endpoint='' AND redeemed_count=0))`, scope.OrgID, runIDs); err != nil {
		return err
	}
	var pending int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM secrets.secret_leases WHERE org_id=$1 AND ($2::uuid[] IS NULL OR run_id=ANY($2))`, scope.OrgID, runIDs).Scan(&pending); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if pending > 0 {
		return fmt.Errorf("credential purge pending: %d leases require revocation or issuer rotation", pending)
	}
	return nil
}
