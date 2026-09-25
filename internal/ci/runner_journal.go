package ci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"google.golang.org/protobuf/proto"
)

var errLogBarrier = errors.New("runner log frames not yet durable")

// lockRunnerJob serializes admission, replacement and terminal receipts. No
// network operation occurs in these transactions. Organization comes from the
// authenticated runner, and is joined to the job's owning run, never a request.
func lockRunnerJob(ctx context.Context, tx pgx.Tx, runner, connection, job uuid.UUID) (string, int64, []byte, error) {
	var org uuid.UUID
	if connection == uuid.Nil {
		return "", 0, nil, errors.New("runner connection required")
	}
	if err := tx.QueryRow(ctx, `SELECT org_id FROM ci.runners WHERE id=$1 AND connection_id=$2 FOR UPDATE`, runner, connection).Scan(&org); err != nil {
		return "", 0, nil, err
	}
	var state string
	var sequence int64
	var receipt []byte
	err := tx.QueryRow(ctx, `SELECT j.status,j.log_sequence,j.terminal_receipt FROM ci.workflow_jobs j JOIN ci.workflow_runs r ON r.id=j.run_id WHERE j.id=$1 AND r.org_id=$2 AND j.runner_id=$3 AND j.connection_id=$4 AND j.journal_enabled FOR UPDATE OF j`, job, org, runner, connection).Scan(&state, &sequence, &receipt)
	return state, sequence, receipt, err
}

func (s *Store) appendRunnerLog(ctx context.Context, runner, connection, job uuid.UUID, sequence int64, line string, digest [32]byte) error {
	if sequence <= 0 || len(line) > 1<<20 {
		return errors.New("invalid log frame")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	state, last, _, err := lockRunnerJob(ctx, tx, runner, connection, job)
	if err != nil {
		return err
	}
	if sequence <= last {
		var prior []byte
		if err := tx.QueryRow(ctx, `SELECT content_sha256 FROM ci.runner_log_lines WHERE job_id=$1 AND sequence=$2`, job, sequence).Scan(&prior); err != nil {
			return err
		}
		if !bytes.Equal(prior, digest[:]) {
			return errors.New("log replay conflicts")
		}
		return nil
	}
	if terminalJobStatus(state) || sequence != last+1 {
		return errors.New("log admission closed or sequence gap")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ci.runner_log_lines(job_id,sequence,line,content_sha256) VALUES($1,$2,$3,$4)`, job, sequence, line, digest[:]); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE ci.workflow_jobs SET log_sequence=$2 WHERE id=$1`, job, sequence); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) recordRunnerReceipt(ctx context.Context, runner, connection, job uuid.UUID, req *civ1.ReportStatusRequest, detail string) error {
	if !validJobStatuses[req.Status] || req.GetLastLogSequence() < 0 || (terminalJobStatus(req.Status) && req.LastLogSequence == nil) {
		return errors.New("invalid runner receipt")
	}
	// The digest binds exact unredacted intent without persisting credential
	// material. It remains stable after the replica forgets its masking context.
	copy := proto.Clone(req).(*civ1.ReportStatusRequest)
	copy.Token = ""
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(copy)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(encoded)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	state, last, prior, err := lockRunnerJob(ctx, tx, runner, connection, job)
	if err != nil {
		return err
	}
	if terminalJobStatus(state) {
		if bytes.Equal(prior, hash[:]) {
			_ = tx.Rollback(ctx)
			return s.rollUpRunStatus(ctx, job)
		}
		return errors.New("terminal receipt conflicts")
	}
	if terminalJobStatus(req.Status) && last != req.GetLastLogSequence() {
		return errLogBarrier
	}
	var receipt []byte
	if terminalJobStatus(req.Status) {
		receipt = hash[:]
	}
	_, err = tx.Exec(ctx, `UPDATE ci.workflow_jobs SET status=$2,detail=$3,finished_at=CASE WHEN $4 THEN now() ELSE finished_at END,terminal_receipt=$5,log_seal_pending=$4 WHERE id=$1`, job, req.Status, detail, terminalJobStatus(req.Status), receipt)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if terminalJobStatus(req.Status) {
		return s.rollUpRunStatus(ctx, job)
	}
	return nil
}

// journalSnapshot is authoritative for jobs admitted by the incarnation
// protocol. Redis is not an authorization database and cannot atomically share
// its write with replacement in PostgreSQL.
func (s *Store) journalSnapshot(ctx context.Context, job uuid.UUID) ([]string, bool, error) {
	var journal, retired bool
	err := s.pool.QueryRow(ctx, `SELECT journal_enabled,logs_retired FROM ci.workflow_jobs WHERE id=$1`, job).Scan(&journal, &retired)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, journal, err
	}
	// Retirement also hides legacy Redis/object evidence and prevents its
	// old sealing path from rematerializing a retired job.
	if retired {
		return []string{}, true, nil
	}
	if !journal {
		return nil, false, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT line FROM ci.runner_log_lines WHERE job_id=$1 ORDER BY sequence`, job)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	lines := []string{}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, true, err
		}
		lines = append(lines, line)
	}
	return lines, true, rows.Err()
}

func (s *LogSink) sealJournal(ctx context.Context, job uuid.UUID) (string, error) {
	if s.blobs == nil {
		return "", errors.New("log object storage unavailable")
	}
	var closed bool
	if err := s.store.pool.QueryRow(ctx, `SELECT status IN ('success','failure','cancelled') AND NOT logs_retired FROM ci.workflow_jobs WHERE id=$1`, job).Scan(&closed); err != nil {
		return "", err
	}
	if !closed {
		return "", errors.New("log admission not closed")
	}
	lines, _, err := s.store.journalSnapshot(ctx, job)
	if err != nil {
		return "", err
	}
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	key := sealedObjectKey(job)
	// All concurrent/restarted projections write identical immutable content:
	// terminal admission has closed, and late frames cannot add to this snapshot.
	if err := s.blobs.Put(ctx, key, strings.NewReader(content), int64(len(content)), "text/plain"); err != nil {
		return "", err
	}
	return s.acknowledgeSeal(ctx, job, key)
}

// Both legacy Redis and journal projection must discharge the same durable
// obligation, and neither may acknowledge across the retention fence.
func (s *LogSink) acknowledgeSeal(ctx context.Context, job uuid.UUID, key string) (string, error) {
	tag, err := s.store.pool.Exec(ctx, `UPDATE ci.workflow_jobs SET log_seal_pending=false WHERE id=$1 AND NOT logs_retired AND status IN ('success','failure','cancelled')`, job)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		// Retirement won during the upload. The retired inventory remains
		// discoverable by the sweeper even if this compensation fails/crashes.
		if err := s.blobs.Delete(ctx, key); err != nil {
			return "", err
		}
		return "", errors.New("log retired during materialization")
	}
	return key, nil
}

// reconcileRunnerLogs retries committed terminal obligations, including after
// process failure. Failed projection never discards authoritative log rows.
func (s *Service) reconcileRunnerLogs(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rows, err := s.Store.pool.Query(ctx, `WITH due AS (SELECT id FROM ci.workflow_jobs WHERE log_seal_pending AND NOT logs_retired AND (log_seal_attempted_at IS NULL OR log_seal_attempted_at<now()-interval '30 seconds') ORDER BY log_seal_attempted_at NULLS FIRST,finished_at,id FOR UPDATE SKIP LOCKED LIMIT 100) UPDATE ci.workflow_jobs j SET log_seal_attempted_at=now() FROM due WHERE j.id=due.id RETURNING j.id`)
			if err != nil {
				continue
			}
			var jobs []uuid.UUID
			for rows.Next() {
				var id uuid.UUID
				if rows.Scan(&id) == nil {
					jobs = append(jobs, id)
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				continue
			}
			for _, job := range jobs {
				if ctx.Err() != nil {
					return
				}
				call, cancel := context.WithTimeout(ctx, 10*time.Second)
				_, err := s.Logs.Seal(call, job)
				cancel()
				if err != nil {
					log.Printf("CI log seal pending for %s: %v", job, err)
				}
			}
		}
	}
}
