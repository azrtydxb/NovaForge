package ci

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/metadata"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// AgentJob is a claimed agent job with everything needed to brief its run.
type AgentJob struct {
	JobID       uuid.UUID
	RunID       uuid.UUID
	Name        string
	Role        string
	OrgID       uuid.UUID
	RepoID      uuid.UUID
	RepoName    string
	CommitSHA   string
	Ref         string
	TriggeredBy uuid.UUID
}

// InFlightAgentJob is an agent job that is running: AgentRunID is uuid.Nil
// when the job was claimed but its run was never recorded as started.
type InFlightAgentJob struct {
	JobID      uuid.UUID
	OrgID      uuid.UUID
	AgentRunID uuid.UUID
	StartedAt  time.Time
}

func nullableUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

// ClaimAgentJob claims one pending agent job, in any organization, whose
// needs have all succeeded, and marks it running. Like ClaimForDispatch it is
// a platform worker's query: the organization comes back with the claimed row
// rather than being taken from a caller, and every later call the worker makes
// for the job is scoped to exactly that organization.
func (s *Store) ClaimAgentJob(ctx context.Context) (AgentJob, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AgentJob{}, fmt.Errorf("begin agent claim: %w", err)
	}
	defer tx.Rollback(ctx)

	var (
		j           AgentJob
		triggeredBy *uuid.UUID
	)
	err = tx.QueryRow(ctx, `
		SELECT j.id, j.run_id, j.name, j.agent_role,
		       r.org_id, r.repo_id, r.repo_name, r.commit_sha, r.ref, r.triggered_by
		FROM ci.workflow_jobs j
		JOIN ci.workflow_runs r ON r.id = j.run_id
		WHERE j.status = 'pending'
		  AND coalesce(j.agent_role, '') <> ''
		  AND NOT EXISTS (
		    SELECT 1 FROM unnest(j.needs) AS need(name)
		    WHERE NOT EXISTS (
		      SELECT 1 FROM ci.workflow_jobs dep
		      WHERE dep.run_id = j.run_id AND dep.name = need.name AND dep.status = 'success'
		    )
		  )
		ORDER BY j.id
		FOR UPDATE OF j SKIP LOCKED
		LIMIT 1`,
	).Scan(&j.JobID, &j.RunID, &j.Name, &j.Role, &j.OrgID, &j.RepoID, &j.RepoName, &j.CommitSHA, &j.Ref, &triggeredBy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentJob{}, ErrNoClaimableJob
		}
		return AgentJob{}, fmt.Errorf("claim agent job: %w", err)
	}
	if triggeredBy != nil {
		j.TriggeredBy = *triggeredBy
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ci.workflow_jobs SET status = 'running', started_at = now() WHERE id = $1`, j.JobID); err != nil {
		return AgentJob{}, fmt.Errorf("mark agent job running: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ci.workflow_runs SET status = 'running' WHERE id = $1 AND status = 'queued'`, j.RunID); err != nil {
		return AgentJob{}, fmt.Errorf("mark run running: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentJob{}, fmt.Errorf("commit agent claim: %w", err)
	}
	return j, nil
}

// SetJobAgentRun records the Agent Run executing jobID and the Work Item it
// was briefed with.
func (s *Store) SetJobAgentRun(ctx context.Context, jobID, agentRunID uuid.UUID, workItemKey string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE ci.workflow_jobs SET agent_run_id = $1, work_item_key = $2 WHERE id = $3`,
		agentRunID, workItemKey, jobID)
	if err != nil {
		return fmt.Errorf("record agent run for job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("job %s not found", jobID)
	}
	return nil
}

// InFlightAgentJobs lists every running agent job, in every organization, for
// the worker to follow.
func (s *Store) InFlightAgentJobs(ctx context.Context) ([]InFlightAgentJob, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT j.id, r.org_id, j.agent_run_id, coalesce(j.started_at, now())
		FROM ci.workflow_jobs j
		JOIN ci.workflow_runs r ON r.id = j.run_id
		WHERE j.status = 'running' AND coalesce(j.agent_role, '') <> ''`)
	if err != nil {
		return nil, fmt.Errorf("list in-flight agent jobs: %w", err)
	}
	defer rows.Close()
	var out []InFlightAgentJob
	for rows.Next() {
		var (
			j     InFlightAgentJob
			runID *uuid.UUID
		)
		if err := rows.Scan(&j.JobID, &j.OrgID, &runID, &j.StartedAt); err != nil {
			return nil, fmt.Errorf("scan in-flight agent job: %w", err)
		}
		if runID != nil {
			j.AgentRunID = *runID
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// AgentJobs executes agent jobs as Agent Runs.
//
// A workflow job declaring `agent: security` used to be pushed to a runner
// like any other job. The runner only knew how to run a shell command, ran an
// empty one, exited 0, and reported success: every agent review in every
// workflow passed without an agent ever being asked. Agent jobs now never
// reach a runner. The platform claims them here and briefs an agent with the
// job's role through a Work Item, the unit every Agent Run works against,
// and the job reports whatever the run concluded.
type AgentJobs struct {
	Store      *Store
	Agents     agentsv1.AgentServiceClient
	Work       workv1.WorkServiceClient
	HMACSecret string
	Log        *slog.Logger
	Interval   time.Duration
	// StartTimeout bounds how long a claimed job may sit without a recorded
	// run — a worker that died between claiming and starting — before it is
	// failed rather than left running forever.
	StartTimeout time.Duration
}

// agentJobInterval is how often agent jobs are claimed and followed. An
// agent run takes minutes; a few seconds of latency is invisible.
const agentJobInterval = 5 * time.Second

// Run claims and follows agent jobs until ctx is cancelled.
func (a *AgentJobs) Run(ctx context.Context) {
	interval := a.Interval
	if interval <= 0 {
		interval = agentJobInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.Tick(ctx); err != nil && ctx.Err() == nil {
				a.logger().Error("ci agent jobs", "error", err)
			}
		}
	}
}

// Tick starts every claimable agent job, then settles every in-flight job
// whose run has finished.
func (a *AgentJobs) Tick(ctx context.Context) error {
	for {
		job, err := a.Store.ClaimAgentJob(ctx)
		if errors.Is(err, ErrNoClaimableJob) {
			break
		}
		if err != nil {
			return err
		}
		if err := a.start(ctx, job); err != nil {
			// The job is failed with the reason rather than retried: every
			// cause here (no agent with the role, nobody to sponsor it, a
			// refused run) is a property of the workflow or the organization
			// that a retry would meet again.
			a.logger().Warn("ci agent job not started", "job", job.JobID, "role", job.Role, "error", err)
			if serr := a.Store.SetJobStatus(ctx, job.JobID, "failure", err.Error()); serr != nil {
				return serr
			}
		}
	}
	return a.follow(ctx)
}

func (a *AgentJobs) start(ctx context.Context, job AgentJob) error {
	callCtx, err := a.orgContext(ctx, job.OrgID)
	if err != nil {
		return err
	}

	// An Agent Run always has a human answerable for it: the member who
	// pushed or asked for the CI run. The scheduler records one only when a
	// person did (a push authenticated as a user, or a user's request); a
	// push made by an agent or a service has nobody, and the job says so
	// rather than borrowing someone's name.
	if job.TriggeredBy == uuid.Nil {
		return fmt.Errorf("agent job %q has no sponsor: no member triggered this CI run (it was pushed by an agent or a service), and an agent run needs a human answerable for it", job.Name)
	}

	agent, err := a.agentForRole(callCtx, job.Role)
	if err != nil {
		return err
	}

	short := job.CommitSHA
	if len(short) > 12 {
		short = short[:12]
	}
	item, err := a.Work.CreateItem(callCtx, &workv1.CreateItemRequest{
		RepoId: job.RepoID.String(),
		Type:   "research",
		Goal: fmt.Sprintf("CI job %q: review commit %s on %s as the %s agent. "+
			"Read the change, judge it from the %s perspective, and record your conclusion as a comment on this Work Item.",
			job.Name, short, strings.TrimPrefix(job.Ref, "refs/heads/"), job.Role, job.Role),
		Acceptance: []string{
			fmt.Sprintf("A comment on this Work Item states the %s review's conclusion for commit %s and the evidence it rests on.", job.Role, short),
			fmt.Sprintf("Commit %s introduces no %s problem that should block it from merging.", short, job.Role),
		},
		Constraints: []string{
			"This is a review: do not commit changes to the repository.",
		},
	})
	if err != nil {
		return fmt.Errorf("create work item for agent job: %w", err)
	}
	key := item.GetItem().GetKey()

	started, err := a.Agents.StartRun(callCtx, &agentsv1.StartRunRequest{
		AgentId:     agent.GetId(),
		RepoId:      job.RepoID.String(),
		WorkItemKey: key,
		SponsorId:   job.TriggeredBy.String(),
	})
	if err != nil {
		return fmt.Errorf("start agent run for %s: %w", key, err)
	}
	runID, err := uuid.Parse(started.GetRun().GetId())
	if err != nil {
		return fmt.Errorf("parse started run id: %w", err)
	}
	if err := a.Store.SetJobAgentRun(ctx, job.JobID, runID, key); err != nil {
		return err
	}
	a.logger().Info("ci agent job started", "job", job.JobID, "role", job.Role, "agent_run", runID, "work_item", key)
	return nil
}

// follow settles each in-flight agent job whose run has reached a terminal
// state, mapping that state onto the job's.
func (a *AgentJobs) follow(ctx context.Context) error {
	jobs, err := a.Store.InFlightAgentJobs(ctx)
	if err != nil {
		return err
	}
	timeout := a.StartTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	for _, j := range jobs {
		if j.AgentRunID == uuid.Nil {
			if time.Since(j.StartedAt) > timeout {
				_ = a.Store.SetJobStatus(ctx, j.JobID, "failure", "the agent run for this job was never started")
			}
			continue
		}
		callCtx, err := a.orgContext(ctx, j.OrgID)
		if err != nil {
			return err
		}
		resp, err := a.Agents.GetRun(callCtx, &agentsv1.GetRunRequest{Id: j.AgentRunID.String()})
		if err != nil {
			a.logger().Warn("ci agent job: read run", "job", j.JobID, "agent_run", j.AgentRunID, "error", err)
			continue
		}
		status, ok := jobStatusForRun(resp.GetRun().GetState())
		if !ok {
			continue
		}
		detail := fmt.Sprintf("agent run %s %s", j.AgentRunID, resp.GetRun().GetState())
		if err := a.Store.SetJobStatus(ctx, j.JobID, status, detail); err != nil {
			return err
		}
	}
	return nil
}

// jobStatusForRun maps an Agent Run's state onto a CI job status, reporting
// false while the run is still going.
func jobStatusForRun(state string) (string, bool) {
	switch state {
	case "succeeded":
		return "success", true
	case "failed", "over_budget":
		return "failure", true
	case "cancelled":
		return "cancelled", true
	default:
		return "", false
	}
}

func (a *AgentJobs) agentForRole(ctx context.Context, role string) (*agentsv1.Agent, error) {
	listed, err := a.Agents.ListAgents(ctx, &agentsv1.ListAgentsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	for _, ag := range listed.GetAgents() {
		if ag.GetEnabled() && ag.GetRole() == role {
			return ag, nil
		}
	}
	return nil, fmt.Errorf("no enabled agent with role %q in this organization", role)
}

// orgContext presents this worker as the platform acting inside exactly one
// organization.
func (a *AgentJobs) orgContext(ctx context.Context, orgID uuid.UUID) (context.Context, error) {
	tok, err := svcauth.Mint(a.HMACSecret, "ci-agent-jobs", orgID, svcauth.DefaultTTL)
	if err != nil {
		return nil, fmt.Errorf("mint service token: %w", err)
	}
	return metadata.AppendToOutgoingContext(ctx,
		"authorization", "Bearer "+tok,
		"x-novaforge-org", orgID.String(),
	), nil
}

func (a *AgentJobs) logger() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}
