package gates

import (
	"context"

	"github.com/google/uuid"
)

// FenceRuns durably refuses subsequent reservation/create/exec/result admission
// for these runs. settled=false is NOT rollback: the fence commits and purge must
// defer. Existing obligations and evidence remain available for scoped cleanup.
func (s *SandboxJournal) FenceRuns(ctx context.Context, runs []uuid.UUID) (settled bool, err error) {
	if len(runs) == 0 {
		return false, ErrSandboxConflict
	}
	for _, run := range runs {
		if run == uuid.Nil {
			return false, ErrSandboxConflict
		}
	}
	return s.fence(ctx, runs)
}

// FenceOrganization applies across all operator targets for the authenticated
// org. No membership, Git/Reviews access or tenant bearer credential is retained.
// This is an internal store method, not an arbitrary-name public cleanup API.
func (s *SandboxJournal) FenceOrganization(ctx context.Context) (settled bool, err error) {
	return s.fence(ctx, nil)
}

func (s *SandboxJournal) fence(ctx context.Context, runs []uuid.UUID) (bool, error) {
	org, err := sandboxOrg(ctx)
	if err != nil {
		return false, err
	}
	tx, err := beginSandboxJournalTx(ctx, s.pool)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background())
	// The same org row serializes admission across targets. Fence never waits on
	// capacity after taking this lock, preserving capacity->org lock ordering.
	if _, err = lockSandboxOrg(ctx, tx, org); err != nil {
		return false, err
	}
	if runs == nil {
		_, err = tx.Exec(ctx, `UPDATE gates.sandbox_org_fences SET deleting=true WHERE org_id=$1`, org)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO gates.sandbox_run_fences (org_id,run_id) SELECT $1,unnest($2::uuid[]) ON CONFLICT DO NOTHING`, org, runs)
	}
	if err != nil {
		return false, err
	}
	var pending bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM gates.sandbox_invocations WHERE org_id=$1 AND NOT released AND ($2::uuid[] IS NULL OR run_id=ANY($2)))`, org, runs).Scan(&pending)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return !pending, nil
}
