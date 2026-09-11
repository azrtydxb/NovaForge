// Package gates owns the gates service's schema: SHA-scoped gate evaluation
// results. The gates service is the sole writer of merge eligibility; no
// other service reads these tables directly.
package gates

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
)

// Definition is a gate as declared in a repository's .novaforge/gates
// directory: a named check with parameters, optionally required for merge.
type Definition struct {
	Name     string
	Params   map[string]any
	Required bool
}

// Evaluation is a row in the gates.gate_evaluations table: the result of
// running one gate against one run's target SHA.
type Evaluation struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	RunID       uuid.UUID
	Gate        string
	Status      string
	Detail      string
	EvaluatedAt time.Time
	TargetSHA   string
}

// Store provides access to the gates schema's gate_evaluations table.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps pool as a gates.Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// RecordEvaluation upserts the result of evaluating e.Gate for e.RunID at
// e.TargetSHA. Upserting on (run_id, gate, target_sha) means an
// at-least-once redelivery of the same evaluation can never create a
// duplicate row: it only ever refreshes the single row for that gate at
// that commit.
func (s *Store) RecordEvaluation(ctx context.Context, e Evaluation) error {
	if err := authz.RequireOrg(ctx, e.OrgID); err != nil {
		return err
	}
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO gates.gate_evaluations (id, org_id, run_id, gate, status, detail, target_sha, evaluated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		 ON CONFLICT (run_id, gate, target_sha) DO UPDATE SET
		   status = EXCLUDED.status,
		   detail = EXCLUDED.detail,
		   evaluated_at = now()`,
		e.ID, e.OrgID, e.RunID, e.Gate, e.Status, e.Detail, e.TargetSHA,
	)
	if err != nil {
		return fmt.Errorf("record gate evaluation: %w", err)
	}
	return nil
}

// ListEvaluations returns every evaluation recorded for runID, scoped to the
// caller's org, most recent first.
func (s *Store) ListEvaluations(ctx context.Context, runID uuid.UUID) ([]Evaluation, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, org_id, run_id, gate, status, detail, target_sha, evaluated_at
		 FROM gates.gate_evaluations WHERE run_id = $1 AND org_id = $2
		 ORDER BY evaluated_at DESC`,
		runID, scope.OrgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list gate evaluations: %w", err)
	}
	defer rows.Close()

	var out []Evaluation
	for rows.Next() {
		var e Evaluation
		if err := rows.Scan(&e.ID, &e.OrgID, &e.RunID, &e.Gate, &e.Status, &e.Detail, &e.TargetSHA, &e.EvaluatedAt); err != nil {
			return nil, fmt.Errorf("scan gate evaluation: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list gate evaluations: %w", err)
	}
	return out, nil
}

// LatestForSHA returns, for each gate that has been evaluated for runID at
// exactly targetSHA, its most recent evaluation. Evaluations are SHA-scoped
// by construction (RecordEvaluation upserts per (run_id, gate, target_sha)),
// so a gate evaluated only at a different SHA is simply absent from the
// result: a new push invalidates prior results without any explicit
// invalidation step.
func (s *Store) LatestForSHA(ctx context.Context, runID uuid.UUID, sha string) (map[string]Evaluation, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, org_id, run_id, gate, status, detail, target_sha, evaluated_at
		 FROM gates.gate_evaluations WHERE run_id = $1 AND org_id = $2 AND target_sha = $3`,
		runID, scope.OrgID, sha,
	)
	if err != nil {
		return nil, fmt.Errorf("latest gate evaluations for sha: %w", err)
	}
	defer rows.Close()

	out := make(map[string]Evaluation)
	for rows.Next() {
		var e Evaluation
		if err := rows.Scan(&e.ID, &e.OrgID, &e.RunID, &e.Gate, &e.Status, &e.Detail, &e.TargetSHA, &e.EvaluatedAt); err != nil {
			return nil, fmt.Errorf("scan gate evaluation: %w", err)
		}
		out[e.Gate] = e
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("latest gate evaluations for sha: %w", err)
	}
	return out, nil
}
