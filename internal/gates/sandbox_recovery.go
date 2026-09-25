package gates

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
)

// SandboxRecoveryRef contains identifiers only. It is not dispatch authority or
// tenant payload; the recovery worker must re-enter OrgID before reading identity.
type SandboxRecoveryRef struct {
	OrgID        uuid.UUID
	InvocationID uuid.UUID
}

// RecoveryPage is an explicit platform-worker query exception: it returns only
// org/invocation IDs for one immutable operator-configured target. It never reads
// another service's schema or filters away deletion-fenced obligations. A sweep
// starts with an empty cursor and uses its last result for subsequent pages;
// periodically restarting the sweep also catches concurrent inserts behind it.
// The caller must authenticate a gates-sandbox-recovery platform identity before
// installing this scope. No RPC or startup worker is wired by this store method.
func (s *SandboxJournal) RecoveryPage(ctx context.Context, after SandboxRecoveryRef, limit int) ([]SandboxRecoveryRef, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !scope.IsPlatformWorker() || scope.PlatformWorker != "gates-sandbox-recovery" {
		return nil, fmt.Errorf("sandbox recovery requires its platform worker")
	}
	if limit < 1 || limit > 128 || (after.OrgID == uuid.Nil) != (after.InvocationID == uuid.Nil) {
		return nil, fmt.Errorf("invalid sandbox recovery page")
	}
	rows, err := s.pool.Query(ctx, `SELECT org_id,id FROM gates.sandbox_invocations
 WHERE target=$1 AND namespace=$2 AND NOT released AND (org_id,id)>($3,$4)
 ORDER BY org_id,id LIMIT $5`, s.target, s.namespace, after.OrgID, after.InvocationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := make([]SandboxRecoveryRef, 0, limit)
	for rows.Next() {
		var ref SandboxRecoveryRef
		if err := rows.Scan(&ref.OrgID, &ref.InvocationID); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return refs, nil
}

// ReadForRecovery resolves an enumerated ID only after organization scope has
// been re-established. The original configured target must still match: changing
// a recovery mapping cannot silently redirect an old invocation. Like ReadExact,
// this read never redeems a persisted create or exec claim for another dispatch.
func (s *SandboxJournal) ReadForRecovery(ctx context.Context, invocation uuid.UUID) (SandboxInvocation, error) {
	org, err := sandboxOrg(ctx)
	if err != nil {
		return SandboxInvocation{}, err
	}
	if invocation == uuid.Nil {
		return SandboxInvocation{}, fmt.Errorf("sandbox invocation ID required")
	}
	v, err := scanSandbox(s.pool.QueryRow(ctx, sandboxSelect, org, invocation))
	if err != nil {
		return SandboxInvocation{}, err
	}
	if v.Identity.OrgID != org || v.Identity.InvocationID != invocation || v.Identity.Target != s.target || v.Identity.Namespace != s.namespace {
		return SandboxInvocation{}, ErrSandboxConflict
	}
	return v, nil
}
