// Package gates owns the gates service's schema: SHA-scoped gate evaluation
// results. The gates service is the sole writer of merge eligibility; no
// other service reads these tables directly.
package gates

import (
	"context"
	"fmt"
	"math"
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
	// TargetSHA is the historical name for the evaluated SOURCE commit.
	TargetSHA string
	// PolicySHA pins the actual target commit supplying policy.
	PolicySHA string
	RepoID    uuid.UUID
	// Nil means no measurement, including legacy rows and failed test runs.
	CoveragePercent *float64
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
// e.TargetSHA under e.PolicySHA. Upserting on
// (run_id, gate, target_sha, policy_sha) deduplicates redelivery without
// replacing evidence evaluated under another policy revision.
func (s *Store) RecordEvaluation(ctx context.Context, e Evaluation) error {
	if err := authz.RequireOrg(ctx, e.OrgID); err != nil {
		return err
	}
	if p := e.CoveragePercent; p != nil && (e.RepoID == uuid.Nil || e.Gate != "tests" ||
		(e.Status != "pass" && e.Status != "fail") || math.IsNaN(*p) || math.IsInf(*p, 0) || *p < 0 || *p > 100) {
		return fmt.Errorf("invalid coverage measurement")
	}
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO gates.gate_evaluations (id, org_id, run_id, gate, status, detail, target_sha, evaluated_at, repo_id, coverage_percent,policy_sha)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, now(), $8, $9,$10)
		 ON CONFLICT (run_id, gate, target_sha, policy_sha) DO UPDATE SET
		   status = EXCLUDED.status,
		   detail = EXCLUDED.detail,
		   repo_id = EXCLUDED.repo_id,
		   coverage_percent = EXCLUDED.coverage_percent,
		   evaluated_at = now()
		 WHERE gates.gate_evaluations.org_id = EXCLUDED.org_id
		   AND (gates.gate_evaluations.repo_id = EXCLUDED.repo_id
		        OR gates.gate_evaluations.repo_id = '00000000-0000-0000-0000-000000000000')`,
		e.ID, e.OrgID, e.RunID, e.Gate, e.Status, e.Detail, e.TargetSHA, e.RepoID, e.CoveragePercent, e.PolicySHA,
	)
	if err != nil {
		return fmt.Errorf("record gate evaluation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("evaluation identity belongs to another scope")
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
		`SELECT id, org_id, run_id, gate, status, detail, target_sha, evaluated_at, repo_id, coverage_percent,policy_sha
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
		if err := rows.Scan(&e.ID, &e.OrgID, &e.RunID, &e.Gate, &e.Status, &e.Detail, &e.TargetSHA, &e.EvaluatedAt, &e.RepoID, &e.CoveragePercent, &e.PolicySHA); err != nil {
			return nil, fmt.Errorf("scan gate evaluation: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list gate evaluations: %w", err)
	}
	return out, nil
}

// LatestForSHA returns each gate's evaluation for the exact source SHA and
// policy SHA. RecordEvaluation upserts per (run_id, gate, target_sha, policy_sha),
// so a source or policy change cannot reuse evidence from another revision.
// Omitting the policy selects only legacy, unqualified evidence.
func (s *Store) LatestForSHA(ctx context.Context, runID uuid.UUID, sha string, policies ...string) (map[string]Evaluation, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	// Absence of a policy pin means legacy evidence only, never an arbitrary
	// qualified policy version that happens to share the source commit.
	policy := ""
	if len(policies) > 0 {
		policy = policies[0]
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, org_id, run_id, gate, status, detail, target_sha, evaluated_at, repo_id, coverage_percent,policy_sha
		 FROM gates.gate_evaluations WHERE run_id = $1 AND org_id = $2 AND target_sha = $3 AND policy_sha = $4`,
		runID, scope.OrgID, sha, policy,
	)
	if err != nil {
		return nil, fmt.Errorf("latest gate evaluations for sha: %w", err)
	}
	defer rows.Close()

	out := make(map[string]Evaluation)
	for rows.Next() {
		var e Evaluation
		if err := rows.Scan(&e.ID, &e.OrgID, &e.RunID, &e.Gate, &e.Status, &e.Detail, &e.TargetSHA, &e.EvaluatedAt, &e.RepoID, &e.CoveragePercent, &e.PolicySHA); err != nil {
			return nil, fmt.Errorf("scan gate evaluation: %w", err)
		}
		out[e.Gate] = e
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("latest gate evaluations for sha: %w", err)
	}
	return out, nil
}
