package deployment

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/novaforge/novaforge/internal/authz"
)

func (s *Service) insert(ctx context.Context, op Operation) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	if scope.OrgID == uuid.Nil || scope.OrgID != op.OrgID {
		return errors.New("deployment requires matching org scope")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// A destination is durably owned by one organization. Never enumerate a
	// foreign organization's operations to decide exclusion or disclose its id.
	if _, err = tx.Exec(ctx, `INSERT INTO deployment.destinations (destination,org_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, op.Destination, scope.OrgID); err != nil {
		return err
	}
	var owned bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployment.destinations WHERE destination=$1 AND org_id=$2)`, op.Destination, scope.OrgID).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO deployment.operations
 (id,org_id,repo_id,run_id,actor_id,actor_kind,target,target_revision,environment,artifact,destination,authorized_until,effective_until)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT (id) DO NOTHING`,
		op.ID, scope.OrgID, op.RepoID, op.RunID, op.ActorID, op.ActorKind, op.Target, op.TargetRevision, op.Environment, op.Artifact, op.Destination, op.AuthorizedUntil, op.EffectiveUntil)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Get returns durable intent and each attempt, scoped to the caller's org.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Operation, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return Operation{}, err
	}
	if scope.OrgID == uuid.Nil {
		return Operation{}, errors.New("deployment requires org scope")
	}
	// Repeatable read prevents an operation state and its attempts coming from
	// different commits when an executor finishes between the two SELECTs.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback(ctx)
	var op Operation
	err = tx.QueryRow(ctx, `SELECT id,org_id,repo_id,run_id,actor_id,actor_kind,target,target_revision,environment,artifact,state,created_at,destination,authorized_until,effective_until
 FROM deployment.operations WHERE org_id=$1 AND id=$2`, scope.OrgID, id).Scan(
		&op.ID, &op.OrgID, &op.RepoID, &op.RunID, &op.ActorID, &op.ActorKind, &op.Target, &op.TargetRevision, &op.Environment, &op.Artifact, &op.State, &op.CreatedAt, &op.Destination, &op.AuthorizedUntil, &op.EffectiveUntil)
	if err != nil {
		return Operation{}, err
	}
	rows, err := tx.Query(ctx, `SELECT number,kind,actor_id,actor_kind,state,started_at,finished_at,result,error,authorized_until,credential_expires_at FROM deployment.attempts
 WHERE org_id=$1 AND operation_id=$2 ORDER BY number`, scope.OrgID, id)
	if err != nil {
		return Operation{}, err
	}
	defer rows.Close()
	op.Attempts = []Attempt{}
	for rows.Next() {
		var a Attempt
		if err = rows.Scan(&a.Number, &a.Kind, &a.ActorID, &a.ActorKind, &a.State, &a.StartedAt, &a.FinishedAt, &a.Result, &a.Error, &a.AuthorizedUntil, &a.CredentialExpiresAt); err != nil {
			return Operation{}, err
		}
		op.Attempts = append(op.Attempts, a)
	}
	if err = rows.Err(); err != nil {
		return Operation{}, err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT attempt,provider_binding,phase,resolved_at FROM deployment.credential_obligations WHERE org_id=$1 AND operation_id=$2 ORDER BY attempt`, scope.OrgID, id)
	if err != nil {
		return Operation{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var c CredentialObligation
		if err = rows.Scan(&c.Attempt, &c.ProviderBinding, &c.Phase, &c.ResolvedAt); err != nil {
			return Operation{}, err
		}
		op.Credentials = append(op.Credentials, c)
	}
	if err = rows.Err(); err != nil {
		return Operation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Operation{}, err
	}
	return op, nil
}

func (s *Service) start(ctx context.Context, conn *pgx.Conn, op Operation, kind string) (int, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return 0, err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE deployment.operations SET state='running' WHERE org_id=$1 AND id=$2 AND (( $3='execute' AND state IN ('pending','failed')) OR ($3 IN ('observe','recover') AND state IN ('running','uncertain')))`, scope.OrgID, op.ID, kind)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() != 1 {
		return 0, ErrConflict
	}
	// An unknown earlier action on this target must be reconciled before any
	// different operation can race it. Also reject a stale failed/pending
	// request after a newer operation has begun: approvals are not rollback grants.
	var blocked bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployment.operations other
 WHERE other.org_id=$1 AND other.destination=$2 AND other.id<>$3 AND
 (other.state IN ('running','uncertain') OR EXISTS(SELECT 1 FROM deployment.credential_obligations c WHERE c.org_id=other.org_id AND c.operation_id=other.id AND c.resolved_at IS NULL) OR (other.created_at>$4 AND other.state<>'pending')))`, scope.OrgID, op.Destination, op.ID, op.CreatedAt).Scan(&blocked)
	if err != nil {
		return 0, err
	}
	if blocked {
		return 0, ErrConflict
	}
	if kind != "execute" {
		if _, err = tx.Exec(ctx, `UPDATE deployment.attempts SET state='uncertain',finished_at=now(),error='execution interrupted; recovery observation follows' WHERE org_id=$1 AND operation_id=$2 AND state='running'`, scope.OrgID, op.ID); err != nil {
			return 0, err
		}
	}
	// Authority is monotonically narrowing across attempts, while each attempt's
	// operator window starts here, not while the request awaits human approval.
	ceiling := op.AuthorizedUntil
	if op.EffectiveUntil != nil {
		ceiling = earlierExpiry(ceiling, *op.EffectiveUntil)
	}
	if op.currentCeiling != nil {
		ceiling = earlierExpiry(ceiling, *op.currentCeiling)
	}
	if _, err = tx.Exec(ctx, `UPDATE deployment.operations SET effective_until=LEAST(effective_until,$3) WHERE org_id=$1 AND id=$2`, scope.OrgID, op.ID, ceiling); err != nil {
		return 0, err
	}
	started := time.Now().UTC().Truncate(time.Microsecond)
	expires := earlierExpiry(ceiling, started.Add(10*time.Minute))
	var number int
	err = tx.QueryRow(ctx, `INSERT INTO deployment.attempts (org_id,operation_id,number,state,kind,actor_id,actor_kind,started_at,authorized_until,credential_expires_at)
 SELECT $1,$2,COALESCE(MAX(number),0)+1,'running',$3,$4,$5,$6,$7,$8 FROM deployment.attempts WHERE org_id=$1 AND operation_id=$2 RETURNING number`, scope.OrgID, op.ID, kind, scope.ActorID, scope.ActorKind, started, ceiling, expires).Scan(&number)
	if err != nil {
		return 0, err
	}
	if h, ok := s.targets[op.Target].Executor.(*HelmExecutor); ok && kind != "recover" {
		_, err = tx.Exec(ctx, `INSERT INTO deployment.credential_obligations (org_id,operation_id,attempt,provider_binding) VALUES ($1,$2,$3,$4)`, scope.OrgID, op.ID, number, h.Revision())
		if err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return number, nil
}

func (s *Service) finish(ctx context.Context, conn *pgx.Conn, op Operation, number int, state string, result Result, errText string) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE deployment.attempts SET state=$1,result=$2,error=$3,finished_at=now()
 WHERE org_id=$4 AND operation_id=$5 AND number=$6 AND state='running'`, state, result, errText, scope.OrgID, op.ID, number)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	tag, err = tx.Exec(ctx, `UPDATE deployment.operations SET state=$1 WHERE org_id=$2 AND id=$3 AND state='running'`, state, scope.OrgID, op.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if state == StateSucceeded {
		if err = enqueueSuccess(ctx, tx, op, number, result); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
