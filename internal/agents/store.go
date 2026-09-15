// Package agents owns the agent-runtime service's schema: agent identities,
// agent runs with provenance, and the append-only tool-call audit log. No
// other service reads these tables directly; cross-service reads go through
// this service's gRPC API.
package agents

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
)

const uniqueViolation = "23505"

// Agent is a row in the agents.agents table: an AI agent identity scoped to
// one organization.
type Agent struct {
	ID       uuid.UUID
	OrgID    uuid.UUID
	Name     string
	Role     string
	ModelRef string
	Enabled  bool
}

// Provenance records who and what produced a run, for evidence rather than
// chain-of-thought.
type Provenance struct {
	AgentName   string
	ModelName   string
	WorkItemKey string
	RunRef      string
	SponsorName string
}

// Run is a row in the agents.agent_runs table: one execution of an agent
// against a work item, bounded by wall-clock, token, and cost limits.
type Run struct {
	ID    uuid.UUID
	OrgID uuid.UUID
	// RepoID is the repository this run works in. Without it the run loop
	// cannot tell the agent which repository it is in, and every repo.* tool
	// is unusable: the column existed from the branch-lock migration onward,
	// but nothing carried it out of the database.
	RepoID          uuid.UUID
	AgentID         uuid.UUID
	WorkItemID      uuid.UUID
	SponsorID       uuid.UUID
	GrantID         uuid.UUID
	Branch          string
	State           string
	StartedAt       time.Time
	EndedAt         *time.Time
	WallclockLimit  time.Duration
	TokenLimit      int64
	CostLimitMicros int64

	// TokensUsed and CostUsedMicros are what the run spent, recorded when it
	// ends; EndReason says why a run that did not succeed ended (which limit
	// stopped it, or what failed). All three are zero until the run ends.
	TokensUsed     int64
	CostUsedMicros int64
	EndReason      string

	// Provenance is populated by GetRun when a run_provenance row exists for
	// this run; it is nil until RecordProvenance has been called.
	Provenance *Provenance
}

// Store provides access to the agents schema's core tables.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps pool as an agents.Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool returns the underlying connection pool, for constructing other
// components (such as AuditLog) that share the same schema's connection.
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}

// terminalStates are agent_runs.state values that no further transition may
// leave.
var terminalStates = map[string]bool{
	"succeeded":   true,
	"failed":      true,
	"cancelled":   true,
	"over_budget": true,
}

// validTransitions is the run state machine: queued may move to running or
// cancelled; running may move to any terminal state; nothing moves out of a
// terminal state.
var validTransitions = map[string]map[string]bool{
	"queued": {
		"running":   true,
		"cancelled": true,
		// A run that could not take its branch lock never ran.
		"failed": true,
	},
	"running": {
		"succeeded":   true,
		"failed":      true,
		"cancelled":   true,
		"over_budget": true,
	},
}

// CreateAgent inserts a new agent row scoped to a.OrgID. The caller's scope
// must match a.OrgID.
func (s *Store) CreateAgent(ctx context.Context, a Agent) (Agent, error) {
	if err := authz.RequireOrg(ctx, a.OrgID); err != nil {
		return Agent{}, err
	}
	a.ID = uuid.New()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO agents.agents (id, org_id, name, role, model_ref, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		a.ID, a.OrgID, a.Name, a.Role, a.ModelRef, a.Enabled,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return Agent{}, fmt.Errorf("agent %q already exists in org %s", a.Name, a.OrgID)
		}
		return Agent{}, fmt.Errorf("create agent: %w", err)
	}
	return a, nil
}

// GetAgent looks up an agent by id, scoped to the caller's org.
func (s *Store) GetAgent(ctx context.Context, id uuid.UUID) (Agent, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return Agent{}, err
	}
	var a Agent
	err = s.pool.QueryRow(ctx,
		`SELECT id, org_id, name, role, model_ref, enabled
		 FROM agents.agents WHERE id = $1 AND org_id = $2`,
		id, scope.OrgID,
	).Scan(&a.ID, &a.OrgID, &a.Name, &a.Role, &a.ModelRef, &a.Enabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Agent{}, fmt.Errorf("agent %s not found: %w", id, err)
		}
		return Agent{}, fmt.Errorf("get agent: %w", err)
	}
	return a, nil
}

// ListAgents lists every agent in the caller's org. The org predicate comes
// from the scope in ctx, never from a caller-supplied argument.
func (s *Store) ListAgents(ctx context.Context) ([]Agent, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, org_id, name, role, model_ref, enabled
		 FROM agents.agents WHERE org_id = $1 ORDER BY name`,
		scope.OrgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()

	var out []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.OrgID, &a.Name, &a.Role, &a.ModelRef, &a.Enabled); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	return out, nil
}

// CreateRun inserts a new agent run, queued, scoped to r.OrgID.
func (s *Store) CreateRun(ctx context.Context, r Run) (Run, error) {
	if err := authz.RequireOrg(ctx, r.OrgID); err != nil {
		return Run{}, err
	}
	r.ID = uuid.New()
	r.State = "queued"

	var workItemID any
	if r.WorkItemID != uuid.Nil {
		workItemID = r.WorkItemID
	}

	wallclockSeconds := int64(r.WallclockLimit / time.Second)
	if wallclockSeconds <= 0 {
		wallclockSeconds = 3600
	}
	tokenLimit := r.TokenLimit
	if tokenLimit <= 0 {
		tokenLimit = 1_000_000
	}
	// No cost limit unless one was asked for. A default used to apply, and
	// with no token ever priced it could never be reached: every run appeared
	// bounded by cost and none was.
	costLimit := r.CostLimitMicros
	if costLimit < 0 {
		costLimit = 0
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO agents.agent_runs
		   (id, org_id, repo_id, agent_id, work_item_id, sponsor_id, grant_id, branch, state,
		    wallclock_limit_seconds, token_limit, cost_limit_micros)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'queued', $9, $10, $11)`,
		r.ID, r.OrgID, r.RepoID, r.AgentID, workItemID, r.SponsorID, r.GrantID, r.Branch,
		wallclockSeconds, tokenLimit, costLimit,
	)
	if err != nil {
		return Run{}, fmt.Errorf("create run: %w", err)
	}
	r.WallclockLimit = time.Duration(wallclockSeconds) * time.Second
	r.TokenLimit = tokenLimit
	r.CostLimitMicros = costLimit
	return r, nil
}

// GetRun looks up a run by id, scoped to the caller's org, joining in
// provenance when it has been recorded.
func (s *Store) GetRun(ctx context.Context, id uuid.UUID) (Run, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return Run{}, err
	}

	var r Run
	var workItemID, sponsorID, grantID uuid.UUID
	var wallclockSeconds int
	var startedAt, endedAt *time.Time
	err = s.pool.QueryRow(ctx,
		`SELECT id, org_id, repo_id, agent_id, COALESCE(work_item_id, '00000000-0000-0000-0000-000000000000'),
		        sponsor_id, grant_id, branch, state, started_at, ended_at,
		        wallclock_limit_seconds, token_limit, cost_limit_micros,
		        tokens_used, cost_used_micros, end_reason
		 FROM agents.agent_runs WHERE id = $1 AND org_id = $2`,
		id, scope.OrgID,
	).Scan(&r.ID, &r.OrgID, &r.RepoID, &r.AgentID, &workItemID, &sponsorID, &grantID, &r.Branch,
		&r.State, &startedAt, &endedAt, &wallclockSeconds, &r.TokenLimit, &r.CostLimitMicros,
		&r.TokensUsed, &r.CostUsedMicros, &r.EndReason)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, fmt.Errorf("run %s not found: %w", id, err)
		}
		return Run{}, fmt.Errorf("get run: %w", err)
	}
	r.WorkItemID = workItemID
	r.SponsorID = sponsorID
	r.GrantID = grantID
	r.WallclockLimit = time.Duration(wallclockSeconds) * time.Second
	if startedAt != nil {
		r.StartedAt = *startedAt
	}
	r.EndedAt = endedAt

	var p Provenance
	var workItemKey *string
	err = s.pool.QueryRow(ctx,
		`SELECT agent_name, model_name, work_item_key, run_ref, sponsor_name
		 FROM agents.run_provenance WHERE run_id = $1`,
		id,
	).Scan(&p.AgentName, &p.ModelName, &workItemKey, &p.RunRef, &p.SponsorName)
	switch {
	case err == nil:
		if workItemKey != nil {
			p.WorkItemKey = *workItemKey
		}
		r.Provenance = &p
	case errors.Is(err, pgx.ErrNoRows):
		// No provenance recorded yet; leave r.Provenance nil.
	default:
		return Run{}, fmt.Errorf("get run provenance: %w", err)
	}

	return r, nil
}

// SetRunState transitions run id to newState, enforcing the run state
// machine: queued may move to running or cancelled, running may move to any
// terminal state, and nothing moves out of a terminal state.
func (s *Store) SetRunState(ctx context.Context, id uuid.UUID, newState string) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var current string
	err = tx.QueryRow(ctx,
		`SELECT state FROM agents.agent_runs WHERE id = $1 AND org_id = $2 FOR UPDATE`,
		id, scope.OrgID,
	).Scan(&current)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("run %s not found: %w", id, err)
		}
		return fmt.Errorf("get run state: %w", err)
	}

	if !validTransitions[current][newState] {
		return fmt.Errorf("invalid transition %s -> %s", current, newState)
	}

	var sqlErr error
	if newState == "running" {
		_, sqlErr = tx.Exec(ctx,
			`UPDATE agents.agent_runs SET state = $1, started_at = now() WHERE id = $2`,
			newState, id,
		)
	} else if terminalStates[newState] {
		_, sqlErr = tx.Exec(ctx,
			`UPDATE agents.agent_runs SET state = $1, ended_at = now() WHERE id = $2`,
			newState, id,
		)
	} else {
		_, sqlErr = tx.Exec(ctx,
			`UPDATE agents.agent_runs SET state = $1 WHERE id = $2`,
			newState, id,
		)
	}
	if sqlErr != nil {
		return fmt.Errorf("set run state: %w", sqlErr)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// RecordProvenance stores the provenance of runID. It may be called once per
// run; a second call for the same run overwrites the first, since provenance
// is set once at the start of the run.
func (s *Store) RecordProvenance(ctx context.Context, runID uuid.UUID, p Provenance) error {
	if _, err := authz.FromContext(ctx); err != nil {
		return err
	}
	var workItemKey any
	if p.WorkItemKey != "" {
		workItemKey = p.WorkItemKey
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO agents.run_provenance (run_id, agent_name, model_name, work_item_key, run_ref, sponsor_name)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (run_id) DO UPDATE SET
		   agent_name = EXCLUDED.agent_name,
		   model_name = EXCLUDED.model_name,
		   work_item_key = EXCLUDED.work_item_key,
		   run_ref = EXCLUDED.run_ref,
		   sponsor_name = EXCLUDED.sponsor_name`,
		runID, p.AgentName, p.ModelName, workItemKey, p.RunRef, p.SponsorName,
	)
	if err != nil {
		return fmt.Errorf("record provenance: %w", err)
	}
	return nil
}

// Stats is one agent's run history: how many runs it has started, how they
// ended, and what they spent. It is the record a person reads before deciding
// whether to keep trusting an agent with work.
type Stats struct {
	AgentID    uuid.UUID
	Runs       int
	Succeeded  int
	Failed     int
	OverBudget int
	Running    int
	TokensUsed int64
	TokenLimit int64
}

// ListStats aggregates run history per agent for the caller's organization.
// It is one query rather than one per agent: a screen showing every agent
// would otherwise issue a request per row.
func (s *Store) ListStats(ctx context.Context) ([]Stats, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.agent_id,
		       count(*),
		       count(*) FILTER (WHERE r.state = 'succeeded'),
		       count(*) FILTER (WHERE r.state = 'failed'),
		       count(*) FILTER (WHERE r.state = 'over_budget'),
		       count(*) FILTER (WHERE r.state IN ('queued', 'running')),
		       coalesce(sum(r.tokens_used), 0),
		       coalesce(sum(r.token_limit), 0)
		FROM agents.agent_runs r
		WHERE r.org_id = $1
		GROUP BY r.agent_id`,
		scope.OrgID)
	if err != nil {
		return nil, fmt.Errorf("list agent stats: %w", err)
	}
	defer rows.Close()

	var out []Stats
	for rows.Next() {
		var st Stats
		if err := rows.Scan(&st.AgentID, &st.Runs, &st.Succeeded, &st.Failed,
			&st.OverBudget, &st.Running, &st.TokensUsed, &st.TokenLimit); err != nil {
			return nil, fmt.Errorf("scan agent stats: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// RunsForWorkItem returns the agent runs started against workItemID, newest
// first. An Engineering Run shows the tool calls of the agent run that
// produced it, and this is how the two are connected.
func (s *Store) RunsForWorkItem(ctx context.Context, workItemID uuid.UUID) ([]uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM agents.agent_runs
		WHERE org_id = $1 AND work_item_id = $2
		ORDER BY started_at DESC NULLS LAST`,
		scope.OrgID, workItemID)
	if err != nil {
		return nil, fmt.Errorf("list runs for work item: %w", err)
	}
	defer rows.Close()

	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan run id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Spend is what a finished run consumed, and why it ended when it did not
// succeed.
type Spend struct {
	Tokens     int64
	CostMicros int64
	Reason     string
}

// RecordSpend persists what a finished run actually consumed. The budget is
// enforced while the run is alive; this is what makes the spend — and, for a
// run stopped over budget, the limit that stopped it — answerable afterwards.
func (s *Store) RecordSpend(ctx context.Context, runID uuid.UUID, spend Spend) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE agents.agent_runs SET tokens_used = $1, cost_used_micros = $2, end_reason = $3
		 WHERE id = $4 AND org_id = $5`,
		spend.Tokens, spend.CostMicros, spend.Reason, runID, scope.OrgID,
	); err != nil {
		return fmt.Errorf("record spend for run %s: %w", runID, err)
	}
	return nil
}
