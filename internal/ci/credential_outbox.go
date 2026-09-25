package ci

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
)

// Preparing is bounded independently of issuer lease lifetime. The recovery
// transaction cancels the still-pending reservation before fencing its attempt;
// StartClaimedJob serializes on that same job row, so a slow live pump cannot
// dispatch after recovery. Dispatched attempts are never recovered by this timer.
const credentialPreparationLimit = 2 * time.Minute

func (s *Store) prepareCredentials(ctx context.Context, org, job, runner, attempt uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id uuid.UUID
	err = tx.QueryRow(ctx, `SELECT j.id FROM ci.workflow_jobs j JOIN ci.workflow_runs r ON r.id=j.run_id WHERE j.id=$1 AND r.org_id=$2 AND j.runner_id=$3 AND j.status='pending' FOR UPDATE OF j`, job, org, runner).Scan(&id)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO ci.credential_cleanup(org_id,job_id,attempt_id,phase,ready_at) VALUES($1,$2,$3,'preparing',now()+$4::interval)`, org, job, attempt, credentialPreparationLimit.String())
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) abandonCredentials(ctx context.Context, org, job, attempt uuid.UUID, leases []uuid.UUID) error {
	if leases == nil {
		leases = []uuid.UUID{}
	}
	tag, err := s.pool.Exec(ctx, `UPDATE ci.credential_cleanup SET phase='cleanup',ready_at=now(),lease_ids=$4 WHERE org_id=$1 AND job_id=$2 AND attempt_id=$3`, org, job, attempt, leases)
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("credential cleanup intent missing")
	}
	return err
}

// RecoverCredentialPreparations revokes dispatch admission before external
// cleanup, not merely because a wall clock says the issuing worker died.
func (s *Store) RecoverCredentialPreparations(ctx context.Context) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || !scope.IsPlatformWorker() || scope.PlatformWorker != "ci-credentials" {
		return errors.New("credential recovery requires platform worker")
	}
	rows, err := s.pool.Query(ctx, `SELECT org_id,job_id FROM ci.credential_cleanup WHERE phase='preparing' AND ready_at<=now() ORDER BY ready_at,org_id,job_id LIMIT 100`)
	if err != nil {
		return err
	}
	var ids []cleanupIdentity
	for rows.Next() {
		var id cleanupIdentity
		if err := rows.Scan(&id.org, &id.job); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		scoped := authz.WithScope(ctx, authz.Scope{OrgID: id.org, ActorKind: "service", ServiceName: "ci-credentials"})
		if err := s.recoverCredentialPreparation(scoped, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) recoverCredentialPreparation(ctx context.Context, id cleanupIdentity) error {
	if err := authz.RequireOrg(ctx, id.org); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE ci.workflow_jobs j SET status='failure',detail='credential preparation expired; cleanup pending',finished_at=now()
 WHERE j.id=$1 AND j.status='pending' AND EXISTS(SELECT 1 FROM ci.credential_cleanup c JOIN ci.workflow_runs r ON r.id=j.run_id WHERE r.org_id=$2 AND c.org_id=r.org_id AND c.job_id=j.id AND c.phase='preparing' AND c.ready_at<=now())`, id.job, id.org)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return s.rollUpRunStatus(ctx, id.job)
	}
	return nil
}

type cleanupIdentity struct{ org, job, attempt uuid.UUID }

// RetryCredentialCleanup discovers identifiers only under platform authority,
// then re-enters each org before touching its obligation or calling Gates.
// The CI outbox survives deleted organizations and jobs. A successful RPC with
// pending provider work is not an acknowledgment. Claims expire after crashes.
func (s *Store) RetryCredentialCleanup(ctx context.Context, b CredentialBroker) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || !scope.IsPlatformWorker() || scope.PlatformWorker != "ci-credentials" {
		return errors.New("credential cleanup requires platform worker")
	}
	if b == nil {
		return ErrCredentialsUnavailable
	}
	if err := s.RecoverCredentialPreparations(ctx); err != nil {
		return err
	}
	rows, err := s.pool.Query(ctx, `SELECT org_id,job_id,attempt_id FROM ci.credential_cleanup WHERE phase='cleanup' AND ready_at<=now() ORDER BY ready_at,org_id,job_id,attempt_id LIMIT 100`)
	if err != nil {
		return err
	}
	var ids []cleanupIdentity
	for rows.Next() {
		var id cleanupIdentity
		if err := rows.Scan(&id.org, &id.job, &id.attempt); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		cctx, cancel := context.WithTimeout(authz.WithScope(context.WithoutCancel(ctx), authz.Scope{OrgID: id.org, ActorKind: "service", ServiceName: "ci-credentials"}), 15*time.Second)
		err := s.retryCredentialCleanup(cctx, b, id)
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (s *Store) retryCredentialCleanup(ctx context.Context, b CredentialBroker, id cleanupIdentity) error {
	if err := authz.RequireOrg(ctx, id.org); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE ci.credential_cleanup SET ready_at=now()+interval '30 seconds' WHERE org_id=$1 AND job_id=$2 AND attempt_id=$3 AND phase='cleanup' AND ready_at<=now()`, id.org, id.job, id.attempt)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	result, err := b.RevokeJobLeases(ctx, id.org, id.job, id.attempt)
	if err != nil {
		return fmt.Errorf("credential cleanup RPC: %w", err)
	}
	if !result.Fenced || result.Pending != 0 {
		return ErrCredentialCleanupPending
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM ci.credential_cleanup WHERE org_id=$1 AND job_id=$2 AND attempt_id=$3 AND phase='cleanup'`, id.org, id.job, id.attempt)
	return err
}

func (p *Pump) runCredentialCleanup(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	worker := authz.WithScope(ctx, authz.Scope{ActorKind: "service", PlatformWorker: "ci-credentials"})
	for {
		if err := p.store.RetryCredentialCleanup(worker, p.Credentials); err != nil && ctx.Err() == nil {
			p.log.Warn("ci credential cleanup incomplete", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
