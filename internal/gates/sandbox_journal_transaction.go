package gates

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// An acknowledged claim may authorize Kubernetes effects. Do not inherit an
// asynchronous commit setting that can acknowledge, then lose, that authority.
// SET LOCAL leaves pooled connection defaults unchanged after the transaction.
func beginSandboxJournalTx(ctx context.Context, pool *pgxpool.Pool) (pgx.Tx, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL synchronous_commit=on"); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
		return nil, err
	}
	return tx, nil
}
