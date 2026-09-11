// Package reviews owns the reviews service's schema: Engineering Runs (pull
// requests that carry plan and proof), their plan steps, proof records,
// comments, and reviews. No other service reads these tables directly.
package reviews

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/authz"
)

// Run is a row in the reviews.runs table.
type Run struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	RepoID     uuid.UUID
	WorkItemID uuid.UUID
	Number     int
	Title      string
	SourceRef  string
	TargetRef  string
	State      string
	AuthorID   uuid.UUID
	AuthorKind string
	AgentName  string
	ModelName  string
	CreatedAt  time.Time
}

// PlanStep is a row in the reviews.run_plan_steps table.
type PlanStep struct {
	RunID   uuid.UUID
	Ordinal int
	Text    string
	State   string
}

// ProofRecord is a row in the reviews.run_proof table.
type ProofRecord struct {
	RunID      uuid.UUID
	Gate       string
	Status     string
	Detail     string
	RecordedAt time.Time
}

// Comment is a row in the reviews.run_comments table.
type Comment struct {
	ID         uuid.UUID
	RunID      uuid.UUID
	AuthorID   uuid.UUID
	AuthorKind string
	Body       string
	CreatedAt  time.Time
}

// Review is a row in the reviews.run_reviews table.
type Review struct {
	RunID        uuid.UUID
	ReviewerID   uuid.UUID
	ReviewerKind string
	Verdict      string
	CreatedAt    time.Time
}

// Store provides access to the reviews schema's tables.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps pool as a reviews.Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// CreateRun inserts a new run, allocating its per-repository number inside
// the insert statement so concurrent creates cannot collide.
func (s *Store) CreateRun(ctx context.Context, run Run) (Run, error) {
	run.ID = uuid.New()
	if run.State == "" {
		run.State = "open"
	}

	var workItemID *uuid.UUID
	if run.WorkItemID != uuid.Nil {
		workItemID = &run.WorkItemID
	}
	var agentName *string
	if run.AgentName != "" {
		agentName = &run.AgentName
	}
	var modelName *string
	if run.ModelName != "" {
		modelName = &run.ModelName
	}

	err := s.pool.QueryRow(ctx, `
		INSERT INTO reviews.runs (
			id, org_id, repo_id, work_item_id, number, title, source_ref, target_ref,
			state, author_id, author_kind, agent_name, model_name
		)
		VALUES (
			$1, $2, $3, $4,
			COALESCE((SELECT MAX(number) FROM reviews.runs WHERE repo_id = $3), 0) + 1,
			$5, $6, $7, $8, $9, $10, $11, $12
		)
		RETURNING number, created_at`,
		run.ID, run.OrgID, run.RepoID, workItemID,
		run.Title, run.SourceRef, run.TargetRef, run.State,
		run.AuthorID, run.AuthorKind, agentName, modelName,
	).Scan(&run.Number, &run.CreatedAt)
	if err != nil {
		return Run{}, fmt.Errorf("create run: %w", err)
	}
	return run, nil
}

// GetRun looks up a run by id, scoped to the org carried in ctx.
func (s *Store) GetRun(ctx context.Context, id uuid.UUID) (Run, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return Run{}, err
	}
	var run Run
	var workItemID *uuid.UUID
	var agentName, modelName *string
	err = s.pool.QueryRow(ctx, `
		SELECT id, org_id, repo_id, work_item_id, number, title, source_ref, target_ref,
		       state, author_id, author_kind, agent_name, model_name, created_at
		FROM reviews.runs WHERE org_id = $1 AND id = $2`,
		scope.OrgID, id,
	).Scan(&run.ID, &run.OrgID, &run.RepoID, &workItemID, &run.Number, &run.Title,
		&run.SourceRef, &run.TargetRef, &run.State, &run.AuthorID, &run.AuthorKind,
		&agentName, &modelName, &run.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, fmt.Errorf("run %s not found: %w", id, err)
		}
		return Run{}, fmt.Errorf("get run: %w", err)
	}
	if workItemID != nil {
		run.WorkItemID = *workItemID
	}
	if agentName != nil {
		run.AgentName = *agentName
	}
	if modelName != nil {
		run.ModelName = *modelName
	}
	return run, nil
}

// ListRuns returns runs for orgID and repoID, optionally filtered by state.
func (s *Store) ListRuns(ctx context.Context, orgID, repoID uuid.UUID, state string) ([]Run, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, repo_id, work_item_id, number, title, source_ref, target_ref,
		       state, author_id, author_kind, agent_name, model_name, created_at
		FROM reviews.runs
		WHERE org_id = $1 AND repo_id = $2 AND ($3 = '' OR state = $3)
		ORDER BY number`,
		orgID, repoID, state,
	)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var runs []Run
	for rows.Next() {
		var run Run
		var workItemID *uuid.UUID
		var agentName, modelName *string
		if err := rows.Scan(&run.ID, &run.OrgID, &run.RepoID, &workItemID, &run.Number, &run.Title,
			&run.SourceRef, &run.TargetRef, &run.State, &run.AuthorID, &run.AuthorKind,
			&agentName, &modelName, &run.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		if workItemID != nil {
			run.WorkItemID = *workItemID
		}
		if agentName != nil {
			run.AgentName = *agentName
		}
		if modelName != nil {
			run.ModelName = *modelName
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	return runs, nil
}

// AddPlanStep appends a plan step to runID.
func (s *Store) AddPlanStep(ctx context.Context, runID uuid.UUID, ordinal int, text, state string) error {
	if state == "" {
		state = "pending"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO reviews.run_plan_steps (run_id, ordinal, text, state)
		VALUES ($1, $2, $3, $4)`,
		runID, ordinal, text, state,
	)
	if err != nil {
		return fmt.Errorf("add plan step: %w", err)
	}
	return nil
}

// SetPlanStepState updates the state of a plan step.
func (s *Store) SetPlanStepState(ctx context.Context, runID uuid.UUID, ordinal int, state string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE reviews.run_plan_steps SET state = $1 WHERE run_id = $2 AND ordinal = $3`,
		state, runID, ordinal,
	)
	if err != nil {
		return fmt.Errorf("set plan step state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("plan step %d for run %s not found", ordinal, runID)
	}
	return nil
}

// RecordProof upserts a proof record on (run_id, gate) so at-least-once
// redelivery of the same gate result does not duplicate rows.
func (s *Store) RecordProof(ctx context.Context, runID uuid.UUID, gate, status, detail string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO reviews.run_proof (run_id, gate, status, detail, recorded_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (run_id, gate)
		DO UPDATE SET status = EXCLUDED.status, detail = EXCLUDED.detail, recorded_at = now()`,
		runID, gate, status, detail,
	)
	if err != nil {
		return fmt.Errorf("record proof: %w", err)
	}
	return nil
}

// ListProof returns every proof record for runID.
func (s *Store) ListProof(ctx context.Context, runID uuid.UUID) ([]ProofRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT run_id, gate, status, detail, recorded_at
		FROM reviews.run_proof WHERE run_id = $1
		ORDER BY gate`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("list proof: %w", err)
	}
	defer rows.Close()

	var proofs []ProofRecord
	for rows.Next() {
		var p ProofRecord
		if err := rows.Scan(&p.RunID, &p.Gate, &p.Status, &p.Detail, &p.RecordedAt); err != nil {
			return nil, fmt.Errorf("scan proof: %w", err)
		}
		proofs = append(proofs, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list proof: %w", err)
	}
	return proofs, nil
}

// AddComment appends a comment to runID.
func (s *Store) AddComment(ctx context.Context, runID, authorID uuid.UUID, authorKind, body string) (Comment, error) {
	c := Comment{
		ID:         uuid.New(),
		RunID:      runID,
		AuthorID:   authorID,
		AuthorKind: authorKind,
		Body:       body,
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO reviews.run_comments (id, run_id, author_id, author_kind, body)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at`,
		c.ID, c.RunID, c.AuthorID, c.AuthorKind, c.Body,
	).Scan(&c.CreatedAt)
	if err != nil {
		return Comment{}, fmt.Errorf("add comment: %w", err)
	}
	return c, nil
}

// SubmitReview records reviewerID's verdict on runID. It rejects
// self-approval — the author of a run may never be its own reviewer,
// regardless of author kind — which is the enforcement point for the
// independent-review requirement.
func (s *Store) SubmitReview(ctx context.Context, runID, reviewerID uuid.UUID, reviewerKind, verdict string) error {
	var authorID uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT author_id FROM reviews.runs WHERE id = $1`, runID).Scan(&authorID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("run %s not found: %w", runID, err)
		}
		return fmt.Errorf("submit review: %w", err)
	}
	if reviewerID == authorID {
		return fmt.Errorf("author cannot approve their own run %s", runID)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO reviews.run_reviews (run_id, reviewer_id, reviewer_kind, verdict)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (run_id, reviewer_id)
		DO UPDATE SET verdict = EXCLUDED.verdict, reviewer_kind = EXCLUDED.reviewer_kind, created_at = now()`,
		runID, reviewerID, reviewerKind, verdict,
	)
	if err != nil {
		return fmt.Errorf("submit review: %w", err)
	}
	return nil
}
