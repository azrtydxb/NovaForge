package secrets

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/novaforge/novaforge/internal/authz"
)

type attemptKey struct{}

func WithAttemptID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, attemptKey{}, id)
}
func attemptID(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(attemptKey{}).(uuid.UUID)
	return id
}

var ErrScopeClosed = errors.New("credential issuance scope closed")

// lockScope serializes reservations and terminal fences before any provider
// I/O. Lock order is organization, run, attempt; no tenant data crosses scopes.
func lockScope(ctx context.Context, tx pgx.Tx, org, run, attempt uuid.UUID) (bool, error) {
	if closed, err := lockOrganization(ctx, tx, org); err != nil || closed {
		return closed, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO secrets.credential_scopes(org_id,run_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, org, run); err != nil {
		return false, err
	}
	var closed bool
	if err := tx.QueryRow(ctx, `SELECT closed FROM secrets.credential_scopes WHERE org_id=$1 AND run_id=$2 FOR UPDATE`, org, run).Scan(&closed); err != nil {
		return false, err
	}
	if closed {
		return true, nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO secrets.credential_attempts(org_id,run_id,attempt_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, org, run, attempt); err != nil {
		return false, err
	}
	err := tx.QueryRow(ctx, `SELECT closed FROM secrets.credential_attempts WHERE org_id=$1 AND run_id=$2 AND attempt_id=$3 FOR UPDATE`, org, run, attempt).Scan(&closed)
	return closed, err
}

type CleanupStatus struct {
	Fenced  bool
	Pending int
}

// RevokeRunLeases durably closes a run (nil attempt) or one failed issuance
// attempt. Provider failures do not roll back the fence. Pending includes
// ambiguous issuance whose response was lost; only actual provider success
// can prove cleanup, never the disappearance of a local grant.
func (b *Broker) RevokeRunLeases(ctx context.Context, run, attempt uuid.UUID) (CleanupStatus, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return CleanupStatus{}, err
	}
	if err := requireOrg(ctx, scope.OrgID); err != nil {
		return CleanupStatus{}, err
	}
	if run == uuid.Nil {
		return CleanupStatus{}, errors.New("run id is required")
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return CleanupStatus{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := lockScope(ctx, tx, scope.OrgID, run, attempt); err != nil {
		return CleanupStatus{}, err
	}
	if attempt == uuid.Nil {
		_, err = tx.Exec(ctx, `UPDATE secrets.credential_scopes SET closed=true WHERE org_id=$1 AND run_id=$2`, scope.OrgID, run)
	} else {
		_, err = tx.Exec(ctx, `UPDATE secrets.credential_attempts SET closed=true WHERE org_id=$1 AND run_id=$2 AND attempt_id=$3`, scope.OrgID, run, attempt)
	}
	if err != nil {
		return CleanupStatus{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE secrets.secret_leases SET revocation_requested=true,provider_ready=false
 WHERE org_id=$1 AND run_id=$2 AND ($3::uuid='00000000-0000-0000-0000-000000000000' OR attempt_id=$3) AND (revoked_at IS NULL OR (provider_endpoint='' AND redeemed_count>0))`, scope.OrgID, run, attempt); err != nil {
		return CleanupStatus{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CleanupStatus{}, err
	}
	err = b.RetryRevocations(ctx)
	var pending int
	countErr := b.pool.QueryRow(ctx, `SELECT count(*) FROM secrets.secret_leases WHERE org_id=$1 AND run_id=$2 AND ($3::uuid='00000000-0000-0000-0000-000000000000' OR attempt_id=$3) AND (revoked_at IS NULL OR (provider_endpoint='' AND redeemed_count>0))`, scope.OrgID, run, attempt).Scan(&pending)
	if countErr != nil {
		return CleanupStatus{Fenced: true, Pending: 1}, errors.Join(err, countErr)
	}
	return CleanupStatus{Fenced: true, Pending: pending}, err
}

// RetryRevocations is the org-scoped outbox consumer. A periodic authenticated
// service caller must retry it even when no new jobs arrive.
func (b *Broker) RetryRevocations(ctx context.Context) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	if err := requireOrg(ctx, scope.OrgID); err != nil {
		return err
	}
	rows, err := b.pool.Query(ctx, `SELECT id FROM secrets.secret_leases WHERE org_id=$1 AND revocation_requested AND (revoked_at IS NULL OR (provider_endpoint='' AND redeemed_count>0)) ORDER BY revocation_attempted_at NULLS FIRST,id LIMIT 100`, scope.OrgID)
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if err := b.Revoke(ctx, id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (b *Broker) revokeProvider(ctx context.Context, org, id uuid.UUID, providerID, endpoint string) error {
	var err error
	if b.provider == nil || b.provider.endpoint != endpoint {
		err = ErrProviderUnavailable
	} else if providerID == "" {
		err = fmt.Errorf("%w: issuance outcome unknown; provider reconciliation required", ErrProviderUnavailable)
	} else {
		err = b.provider.revoke(ctx, providerID)
	}
	if err != nil {
		_, recordErr := b.pool.Exec(ctx, `UPDATE secrets.secret_leases SET revocation_error=$3 WHERE org_id=$1 AND id=$2`, org, id, err.Error())
		return errors.Join(err, recordErr)
	}
	_, err = b.pool.Exec(ctx, `UPDATE secrets.secret_leases SET revoked_at=now(),issued_ciphertext=NULL,revocation_error='' WHERE org_id=$1 AND id=$2`, org, id)
	return err
}
