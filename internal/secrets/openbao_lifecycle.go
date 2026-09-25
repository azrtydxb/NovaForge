package secrets

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
)

func (b *Broker) issueDynamic(ctx context.Context, runID, orgID uuid.UUID, binding OpenBaoBinding, ceiling time.Time) (Lease, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return Lease{}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Lease{}, err
	}
	lease := Lease{ID: uuid.New(), Token: hex.EncodeToString(raw), Name: binding.Name, Environment: binding.Environment}
	hash := sha256.Sum256([]byte(lease.Token))
	// A pending row precedes the external operation. Never expose its token or
	// credential until provider metadata is validated and committed.
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback(ctx)
	closed, err := lockScope(ctx, tx, orgID, runID, attemptID(ctx))
	if err != nil {
		return Lease{}, err
	}
	if closed {
		return Lease{}, ErrScopeClosed
	}
	_, err = tx.Exec(ctx, `INSERT INTO secrets.secret_leases
 (id,org_id,run_id,name,token_hash,expires_at,environment,provider_endpoint,attempt_id,actor_id,actor_kind,service_name)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, lease.ID, orgID, runID, lease.Name, hash[:], ceiling, lease.Environment, b.provider.endpoint, attemptID(ctx), scope.ActorID, scope.ActorKind, scope.ServiceName)
	if err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Lease{}, err
	}
	var issued baoResponse
	issueErr := b.provider.call(ctx, binding.Method, binding.Path, binding.Parameters, &issued)
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if issueErr != nil {
		_, recordErr := b.pool.Exec(cleanupCtx, `UPDATE secrets.secret_leases SET revocation_requested=true,revocation_error='issuance outcome unknown; provider reconciliation required' WHERE id=$1 AND org_id=$2`, lease.ID, orgID)
		return Lease{}, errors.Join(issueErr, recordErr)
	}
	if issued.LeaseID == "" {
		_, err := b.pool.Exec(cleanupCtx, `UPDATE secrets.secret_leases SET revocation_requested=true,revocation_error='provider returned no dynamic lease; cleanup cannot be proven' WHERE id=$1 AND org_id=$2`, lease.ID, orgID)
		return Lease{}, errors.Join(ErrProviderContract, err)
	}
	// Persist the revocation handle before any subsequent network call. If
	// validation fails and revocation is unavailable, an operator can retry
	// Revoke against this pending row. Target hard expiry is not inferred.

	tag, err := b.pool.Exec(cleanupCtx, `UPDATE secrets.secret_leases SET provider_lease_id=$3 WHERE id=$1 AND org_id=$2`, lease.ID, orgID, issued.LeaseID)
	if err != nil || tag.RowsAffected() != 1 {
		// A missing reservation is never success. Restore a non-deliverable
		// outbox record before direct compensation, so a provider outage does
		// not discard the only known handle. No issuance is retried here.
		// The failed handle update may have exhausted cleanupCtx. Recovery
		// gets its own deadline rather than silently reusing a canceled one.
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		recordTag, recordErr := b.pool.Exec(persistCtx, `INSERT INTO secrets.secret_leases
 (id,org_id,run_id,name,token_hash,expires_at,environment,provider_endpoint,provider_lease_id,attempt_id,actor_id,actor_kind,service_name,revocation_requested)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,true)
 ON CONFLICT(id) DO UPDATE SET provider_lease_id=EXCLUDED.provider_lease_id,revocation_requested=true,provider_ready=false
 WHERE secrets.secret_leases.org_id=EXCLUDED.org_id`, lease.ID, orgID, runID, lease.Name, hash[:], ceiling, lease.Environment, b.provider.endpoint, issued.LeaseID, attemptID(ctx), scope.ActorID, scope.ActorKind, scope.ServiceName)
		persistCancel()
		if recordErr == nil && recordTag.RowsAffected() != 1 {
			recordErr = ErrProviderContract
		}
		revokeCtx, revokeCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer revokeCancel()
		// Calls the provider using the response handle, not a row lookup.
		revokeErr := b.revokeProvider(revokeCtx, orgID, lease.ID, issued.LeaseID, b.provider.endpoint)
		return Lease{}, errors.Join(ErrProviderContract, err, recordErr, revokeErr)
	}
	fail := func(cause error) (Lease, error) {
		// Issuance/lookup may have consumed their entire timeout; cleanup must
		// still receive its own bounded opportunity to persist revocation intent.
		revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		return Lease{}, errors.Join(cause, b.Revoke(revokeCtx, lease.ID))
	}
	if issued.LeaseDuration <= 0 {
		return fail(ErrProviderContract)
	}
	expires, err := b.provider.expiry(ctx, issued.LeaseID)
	if err != nil {
		return fail(err)
	}
	// PostgreSQL stores microsecond precision. Reject rather than publishing an
	// earlier made-up expiry; the issuing authority must respect the ceiling.
	expires = expires.Truncate(time.Microsecond)
	if !time.Now().Before(expires) || expires.After(ceiling) {
		return fail(ErrProviderContract)
	}
	value, err := mappedCredential(binding, issued.Data)
	if err != nil {
		return fail(err)
	}
	encrypted, err := b.encrypt(value)
	if err != nil {
		return fail(err)
	}
	tag, err = b.pool.Exec(ctx, `UPDATE secrets.secret_leases SET expires_at=$3,issued_ciphertext=$4,provider_ready=true
 WHERE id=$1 AND org_id=$2 AND revoked_at IS NULL AND NOT revocation_requested`, lease.ID, orgID, expires, encrypted)
	if err != nil {
		return fail(err)
	}
	if tag.RowsAffected() != 1 {
		return fail(ErrProviderContract)
	}
	lease.ExpiresAt = expires
	return lease, nil
}
