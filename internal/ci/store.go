// Package ci owns workflow definition parsing, the CI schema, the push-event
// scheduler, runner dispatch over pushed gRPC streams, live and sealed job
// logs, and artifact storage.
package ci

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

// Run is a row in the ci.workflow_runs table: one scheduled evaluation of a
// repository's .novaforge/workflow.yaml at a specific commit.
type Run struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	RepoID    uuid.UUID
	CommitSHA string
	Ref       string
	Status    string
	CreatedAt time.Time
}

// Job is a row in the ci.workflow_jobs table: one job of a Run, either a
// shell command or an agent role, gated on the jobs named in Needs.
type WorkflowJob struct {
	ID         uuid.UUID
	RunID      uuid.UUID
	Name       string
	Needs      []string
	RunCmd     string
	AgentRole  string
	Image      string
	Status     string
	RunnerID   *uuid.UUID
	Detail     string
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// ErrNoClaimableJob is returned by ClaimJob when no pending job with
// satisfied needs is currently available.
var ErrNoClaimableJob = errors.New("no claimable job")

var validJobStatuses = map[string]bool{
	"pending":   true,
	"running":   true,
	"success":   true,
	"failure":   true,
	"cancelled": true,
}

// Store provides access to the ci schema's tables.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps pool as a ci.Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// CreateRun inserts run, allocating a random id when run.ID is zero. Because
// run creation must tolerate at-least-once redelivery of the same push
// event, the insert is an upsert-free ON CONFLICT DO NOTHING keyed on
// (repo_id, commit_sha, ref): a zero-row result means a run for that commit
// and ref already exists, reported to the caller as created == false rather
// than an error.
func (s *Store) CreateRun(ctx context.Context, run Run) (Run, bool, error) {
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	if run.Status == "" {
		run.Status = "queued"
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ci.workflow_runs (id, org_id, repo_id, commit_sha, ref, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (repo_id, commit_sha, ref) DO NOTHING
		RETURNING id, created_at`,
		run.ID, run.OrgID, run.RepoID, run.CommitSHA, run.Ref, run.Status,
	).Scan(&run.ID, &run.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, false, nil
		}
		return Run{}, false, fmt.Errorf("create run: %w", err)
	}
	return run, true, nil
}

// GetRun looks up a run by id, scoped to the org carried in ctx.
func (s *Store) GetRun(ctx context.Context, id uuid.UUID) (Run, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return Run{}, err
	}
	var run Run
	err = s.pool.QueryRow(ctx, `
		SELECT id, org_id, repo_id, commit_sha, ref, status, created_at
		FROM ci.workflow_runs WHERE org_id = $1 AND id = $2`,
		scope.OrgID, id,
	).Scan(&run.ID, &run.OrgID, &run.RepoID, &run.CommitSHA, &run.Ref, &run.Status, &run.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, fmt.Errorf("run %s not found: %w", id, err)
		}
		return Run{}, fmt.Errorf("get run: %w", err)
	}
	return run, nil
}

// ListRuns returns runs for orgID and repoID, most recent first.
func (s *Store) ListRuns(ctx context.Context, orgID, repoID uuid.UUID) ([]Run, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, repo_id, commit_sha, ref, status, created_at
		FROM ci.workflow_runs
		WHERE org_id = $1 AND repo_id = $2
		ORDER BY created_at DESC`,
		orgID, repoID,
	)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var runs []Run
	for rows.Next() {
		var run Run
		if err := rows.Scan(&run.ID, &run.OrgID, &run.RepoID, &run.CommitSHA, &run.Ref, &run.Status, &run.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	return runs, nil
}

// CreateJob inserts a job belonging to a run, defaulting Status to pending.
func (s *Store) CreateJob(ctx context.Context, job WorkflowJob) (WorkflowJob, error) {
	if job.ID == uuid.Nil {
		job.ID = uuid.New()
	}
	if job.Status == "" {
		job.Status = "pending"
	}
	if job.Needs == nil {
		job.Needs = []string{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ci.workflow_jobs (id, run_id, name, needs, run_cmd, agent_role, image, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		job.ID, job.RunID, job.Name, job.Needs,
		nullString(job.RunCmd), nullString(job.AgentRole), nullString(job.Image), job.Status,
	)
	if err != nil {
		return WorkflowJob{}, fmt.Errorf("create job: %w", err)
	}
	return job, nil
}

// ClaimJob atomically claims one pending job whose needs are all satisfied
// (every job it names has succeeded within the same run) and assigns it to
// runnerID. FOR UPDATE SKIP LOCKED ensures two runners claiming
// concurrently never receive the same job. labels is accepted for future
// label-matching against per-job requirements; the current schema carries
// no such requirement on workflow_jobs, so every pending, unblocked job is
// eligible regardless of labels.
func (s *Store) ClaimJob(ctx context.Context, runnerID uuid.UUID, labels []string) (WorkflowJob, error) {
	_ = labels

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WorkflowJob{}, fmt.Errorf("begin claim: %w", err)
	}
	defer tx.Rollback(ctx)

	var job WorkflowJob
	var runCmd, agentRole, image *string
	err = tx.QueryRow(ctx, `
		SELECT j.id, j.run_id, j.name, j.needs, j.run_cmd, j.agent_role, j.image, j.status
		FROM ci.workflow_jobs j
		WHERE j.status = 'pending'
		  AND NOT EXISTS (
		    SELECT 1 FROM unnest(j.needs) AS need(name)
		    WHERE NOT EXISTS (
		      SELECT 1 FROM ci.workflow_jobs dep
		      WHERE dep.run_id = j.run_id AND dep.name = need.name AND dep.status = 'success'
		    )
		  )
		ORDER BY j.id
		FOR UPDATE SKIP LOCKED
		LIMIT 1`,
	).Scan(&job.ID, &job.RunID, &job.Name, &job.Needs, &runCmd, &agentRole, &image, &job.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WorkflowJob{}, ErrNoClaimableJob
		}
		return WorkflowJob{}, fmt.Errorf("claim job: %w", err)
	}
	if runCmd != nil {
		job.RunCmd = *runCmd
	}
	if agentRole != nil {
		job.AgentRole = *agentRole
	}
	if image != nil {
		job.Image = *image
	}

	if _, err := tx.Exec(ctx, `
		UPDATE ci.workflow_jobs SET status = 'running', runner_id = $1, started_at = now()
		WHERE id = $2`,
		runnerID, job.ID,
	); err != nil {
		return WorkflowJob{}, fmt.Errorf("claim job: mark running: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return WorkflowJob{}, fmt.Errorf("claim job: commit: %w", err)
	}

	job.Status = "running"
	job.RunnerID = &runnerID
	return job, nil
}

// SetJobStatus updates a job's status and, for a terminal status, its
// finished_at and detail (an explanatory message, e.g. "runner
// disconnected"; pass "" to leave any existing detail untouched).
func (s *Store) SetJobStatus(ctx context.Context, jobID uuid.UUID, status string, detail string) error {
	if !validJobStatuses[status] {
		return fmt.Errorf("invalid job status %q", status)
	}
	terminal := status == "success" || status == "failure" || status == "cancelled"
	tag, err := s.pool.Exec(ctx, `
		UPDATE ci.workflow_jobs
		SET status = $1,
		    finished_at = CASE WHEN $2 THEN now() ELSE finished_at END,
		    detail = CASE WHEN $3 <> '' THEN $3 ELSE detail END
		WHERE id = $4`,
		status, terminal, detail, jobID,
	)
	if err != nil {
		return fmt.Errorf("set job status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("job %s not found", jobID)
	}
	return nil
}

// GetJob looks up a job by id, unscoped by org since jobs carry no org_id of
// their own — callers scope through the job's run.
func (s *Store) GetJob(ctx context.Context, jobID uuid.UUID) (WorkflowJob, error) {
	var job WorkflowJob
	var runCmd, agentRole, image, detail *string
	err := s.pool.QueryRow(ctx, `
		SELECT id, run_id, name, needs, run_cmd, agent_role, image, status, runner_id, detail, started_at, finished_at
		FROM ci.workflow_jobs WHERE id = $1`,
		jobID,
	).Scan(&job.ID, &job.RunID, &job.Name, &job.Needs, &runCmd, &agentRole, &image, &job.Status,
		&job.RunnerID, &detail, &job.StartedAt, &job.FinishedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WorkflowJob{}, fmt.Errorf("job %s not found: %w", jobID, err)
		}
		return WorkflowJob{}, fmt.Errorf("get job: %w", err)
	}
	if runCmd != nil {
		job.RunCmd = *runCmd
	}
	if agentRole != nil {
		job.AgentRole = *agentRole
	}
	if image != nil {
		job.Image = *image
	}
	if detail != nil {
		job.Detail = *detail
	}
	return job, nil
}

// ListJobsForRun returns every job belonging to runID, ordered by name.
func (s *Store) ListJobsForRun(ctx context.Context, runID uuid.UUID) ([]WorkflowJob, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_id, name, needs, run_cmd, agent_role, image, status, runner_id, detail, started_at, finished_at
		FROM ci.workflow_jobs WHERE run_id = $1 ORDER BY name`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("list jobs for run: %w", err)
	}
	defer rows.Close()

	var jobs []WorkflowJob
	for rows.Next() {
		var job WorkflowJob
		var runCmd, agentRole, image, detail *string
		if err := rows.Scan(&job.ID, &job.RunID, &job.Name, &job.Needs, &runCmd, &agentRole, &image, &job.Status,
			&job.RunnerID, &detail, &job.StartedAt, &job.FinishedAt); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		if runCmd != nil {
			job.RunCmd = *runCmd
		}
		if agentRole != nil {
			job.AgentRole = *agentRole
		}
		if image != nil {
			job.Image = *image
		}
		if detail != nil {
			job.Detail = *detail
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list jobs for run: %w", err)
	}
	return jobs, nil
}

// RunningJobsForRunner returns the ids of every job currently running
// against runnerID, used by the dispatch reaper to fail them when the
// runner's stream breaks.
func (s *Store) RunningJobsForRunner(ctx context.Context, runnerID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM ci.workflow_jobs WHERE runner_id = $1 AND status = 'running'`,
		runnerID,
	)
	if err != nil {
		return nil, fmt.Errorf("running jobs for runner: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan job id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
