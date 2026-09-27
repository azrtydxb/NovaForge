package webhooks

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/novaforge/novaforge/internal/authz"
)

type attemptClaim struct {
	ID     uuid.UUID
	Number int
}

func (s *Store) reserveAttempt(ctx context.Context, hook uuid.UUID, event, delivery string, max int) (attemptClaim, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return attemptClaim{}, err
	}
	if err = authz.RequireOrg(ctx, scope.OrgID); err != nil {
		return attemptClaim{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return attemptClaim{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	_, err = tx.Exec(ctx, `INSERT INTO gitplatform.hook_dispatch(hook_id,delivery,event)
 SELECT id,$2,$3 FROM gitplatform.hooks WHERE id=$1 AND org_id=$4 AND active
 ON CONFLICT DO NOTHING`, hook, delivery, event, scope.OrgID)
	if err != nil {
		return attemptClaim{}, err
	}
	claim := attemptClaim{ID: uuid.New()}
	err = tx.QueryRow(ctx, `UPDATE gitplatform.hook_dispatch d SET attempts=attempts+1,claim=$5,lease_until=now()+interval '30 seconds'
 FROM gitplatform.hooks h WHERE h.id=d.hook_id AND h.org_id=$4 AND h.active
 AND d.hook_id=$1 AND d.delivery=$2 AND NOT d.done AND d.attempts<$3 AND d.lease_until<now()
 RETURNING d.attempts`, hook, delivery, max, scope.OrgID, claim.ID).Scan(&claim.Number)
	if errors.Is(err, pgx.ErrNoRows) {
		var done bool
		var attempts int
		var leased bool
		err = tx.QueryRow(ctx, `SELECT d.done,d.attempts,d.lease_until>now() FROM gitplatform.hook_dispatch d
 JOIN gitplatform.hooks h ON h.id=d.hook_id WHERE d.hook_id=$1 AND d.delivery=$2 AND h.org_id=$3 AND h.active`, hook, delivery, scope.OrgID).Scan(&done, &attempts, &leased)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (done || (attempts >= max && !leased))) {
			return attemptClaim{}, nil
		}
		if err != nil {
			return attemptClaim{}, err
		}
		return attemptClaim{}, fmt.Errorf("delivery has an active attempt")
	}
	if err != nil {
		return attemptClaim{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO gitplatform.hook_deliveries(id,hook_id,event,attempt,error) VALUES($1,$2,$3,$4,'attempt reserved; outcome not yet observed')`, claim.ID, hook, event, claim.Number)
	if err != nil {
		return attemptClaim{}, err
	}
	return claim, tx.Commit(ctx)
}

func (s *Store) finishAttempt(ctx context.Context, hook uuid.UUID, delivery string, claim attemptClaim, code int, detail string, max int) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	tag, err := tx.Exec(ctx, `UPDATE gitplatform.hook_dispatch d SET done=$4,lease_until='-infinity',claim=NULL
 FROM gitplatform.hooks h WHERE h.id=d.hook_id AND h.org_id=$5 AND d.hook_id=$1 AND d.delivery=$2 AND d.claim=$3`, hook, delivery, claim.ID, (code >= 200 && code < 300) || claim.Number >= max, scope.OrgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("delivery claim no longer belongs to worker")
	}
	_, err = tx.Exec(ctx, `UPDATE gitplatform.hook_deliveries SET status_code=$2,error=$3 WHERE id=$1 AND hook_id=$4`, claim.ID, code, detail, hook)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
