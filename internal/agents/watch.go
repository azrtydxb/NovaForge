package agents

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type cancellationWatchKey struct{}

// WithRunCancellation interrupts in-flight work even when CancelRun reached
// another replica. The row remains authoritative, including repository purge.
// Reads are bounded so a database outage cannot pin cancellation indefinitely;
// the run's wall-clock deadline still bounds work during an outage.
func (s *Store) WithRunCancellation(ctx context.Context, id uuid.UUID) (context.Context, context.CancelFunc) {
	if watched, _ := ctx.Value(cancellationWatchKey{}).(uuid.UUID); watched == id {
		return context.WithCancel(ctx)
	}
	ctx, cancel := context.WithCancel(context.WithValue(ctx, cancellationWatchKey{}, id))
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			readCtx, stop := context.WithTimeout(ctx, 2*time.Second)
			run, err := s.GetRun(readCtx, id)
			stop()
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && terminalStates[run.State]) {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return ctx, cancel
}
