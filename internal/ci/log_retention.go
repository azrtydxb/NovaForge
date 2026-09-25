package ci

import (
	"context"
	"errors"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/retention"
)

func retentionScope(ctx context.Context, id retention.LogIdentity) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID != id.OrgID || scope.ActorKind != "service" || scope.ServiceName != "ci-retention" {
		return errors.New("retention owner authority required")
	}
	return nil
}

func (s *Store) retireRunnerLog(ctx context.Context, id retention.LogIdentity) error {
	if err := retentionScope(ctx, id); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE ci.workflow_jobs j SET logs_retired=true,log_seal_pending=false FROM ci.workflow_runs r WHERE j.id=$1 AND j.run_id=r.id AND r.org_id=$2 AND j.connection_id IS NOT DISTINCT FROM $3::uuid AND j.status IN ('success','failure','cancelled')`, id.JobID, id.OrgID, id.ConnectionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("retention generation mismatch")
	}
	return nil
}

func (s *Store) deleteRetiredRunnerLog(ctx context.Context, id retention.LogIdentity) error {
	if err := retentionScope(ctx, id); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var retired bool
	if err := tx.QueryRow(ctx, `SELECT j.logs_retired FROM ci.workflow_jobs j JOIN ci.workflow_runs r ON r.id=j.run_id WHERE j.id=$1 AND r.org_id=$2 AND j.connection_id IS NOT DISTINCT FROM $3::uuid FOR UPDATE OF j`, id.JobID, id.OrgID, id.ConnectionID).Scan(&retired); err != nil {
		return err
	}
	if !retired {
		return errors.New("retention intent missing")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ci.runner_log_lines WHERE job_id=$1`, id.JobID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
