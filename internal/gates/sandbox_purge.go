package gates

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrSandboxPurgePending keeps deletion retryable without discarding the
// original execution identity, cleanup receipts or reserved capacity.
var ErrSandboxPurgePending = errors.New("sandbox obligations require terminal evidence and confirmed cleanup")

func (p *Purger) fenceSandbox(ctx context.Context, runs []uuid.UUID) error {
	// Fencing deliberately has no configured execution target: deletion must
	// cover retired targets too, even when new sandbox execution is disabled.
	// This pool-only view is local to fencing and never admits execution.
	journal := &SandboxJournal{pool: p.Pool}
	var settled bool
	var err error
	if runs == nil {
		settled, err = journal.FenceOrganization(ctx)
	} else {
		settled, err = journal.FenceRuns(ctx, runs)
	}
	if err != nil {
		return err
	}
	if !settled {
		return ErrSandboxPurgePending
	}
	return nil
}
