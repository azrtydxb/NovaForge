package ci

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// BeginRunnerConnection replaces authority, not just a process-local channel.
// Prior jobs are interrupted, not claimed physically stopped. Their credential
// cleanup is committed by the job trigger before a replacement can claim work.
func (s *Store) BeginRunnerConnection(ctx context.Context, runner uuid.UUID) (uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)
	var org uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT org_id FROM ci.runners WHERE id=$1 FOR UPDATE`, runner).Scan(&org); err != nil {
		return uuid.Nil, err
	}
	if err := interruptRunnerJobs(ctx, tx, org, runner, nil, "runner connection replaced; execution interrupted, termination unconfirmed"); err != nil {
		return uuid.Nil, err
	}

	id := uuid.New()
	if _, err := tx.Exec(ctx, `UPDATE ci.runners SET connection_id=$3,last_seen_at=now() WHERE org_id=$1 AND id=$2`, org, runner, id); err != nil {
		return uuid.Nil, err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) EndRunnerConnection(ctx context.Context, runner, connection uuid.UUID) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(cleanup)
	if err != nil {
		return err
	}
	defer tx.Rollback(cleanup)
	var org uuid.UUID
	err = tx.QueryRow(cleanup, `UPDATE ci.runners SET connection_id=NULL WHERE id=$1 AND connection_id=$2 RETURNING org_id`, runner, connection).Scan(&org)
	if err != nil {
		return err
	}
	if err := interruptRunnerJobs(cleanup, tx, org, runner, &connection, "runner disconnected; execution interrupted, termination unconfirmed"); err != nil {
		return err
	}

	return tx.Commit(cleanup)
}

func (s *Store) runnerConnection(ctx context.Context, runner, connection uuid.UUID) error {
	if connection == uuid.Nil {
		return errors.New("runner connection required")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE ci.runners SET last_seen_at=now() WHERE id=$1 AND connection_id=$2`, runner, connection)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("runner connection superseded")
	}
	return nil
}

func (s *Store) jobConnection(ctx context.Context, job, runner, connection uuid.UUID) error {
	if connection == uuid.Nil {
		return errors.New("runner connection required")
	}
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ci.workflow_jobs j JOIN ci.workflow_runs r ON r.id=j.run_id JOIN ci.runners n ON n.id=j.runner_id AND n.org_id=r.org_id WHERE j.id=$1 AND n.id=$2 AND j.connection_id=$3 AND n.connection_id=$3)`, job, runner, connection).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("job connection superseded")
	}
	return nil
}

// Job interruption and run settlement commit together. Lock runs in stable
// order, then take a fresh statement snapshot so concurrent interruptions of
// different runners in the same workflow cannot both miss the final result.
func interruptRunnerJobs(ctx context.Context, tx pgx.Tx, org, runner uuid.UUID, connection *uuid.UUID, detail string) error {
	rows, err := tx.Query(ctx, `UPDATE ci.workflow_jobs j SET status='failure',finished_at=now(),log_seal_pending=true,detail=$4 FROM ci.workflow_runs r WHERE r.id=j.run_id AND r.org_id=$1 AND j.runner_id=$2 AND ($3::uuid IS NULL OR j.connection_id=$3) AND j.status IN ('pending','running') RETURNING j.run_id`, org, runner, connection, detail)
	if err != nil {
		return err
	}
	var runs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return nil
	}
	rows, err = tx.Query(ctx, `SELECT id FROM ci.workflow_runs WHERE org_id=$1 AND id=ANY($2) ORDER BY id FOR UPDATE`, org, runs)
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE ci.workflow_runs r SET status=CASE WHEN EXISTS(SELECT 1 FROM ci.workflow_jobs j WHERE j.run_id=r.id AND j.status IN ('failure','cancelled')) THEN 'failure' ELSE 'success' END WHERE r.org_id=$1 AND r.id=ANY($2) AND r.status NOT IN ('success','failure','cancelled') AND NOT EXISTS(SELECT 1 FROM ci.workflow_jobs j WHERE j.run_id=r.id AND j.status IN ('pending','running'))`, org, runs)
	return err
}
