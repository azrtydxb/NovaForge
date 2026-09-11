package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/gitops"
)

// BranchLock enforces that an agent branch is held by at most one running
// agent run at a time. It is backed by the partial unique index
// agent_branch_lock on agent_runs(org_id, repo_id, branch) WHERE state =
// 'running' (see migrations/000003_branch_lock.up.sql): the database, not
// application code, is what refuses a second acquire.
type BranchLock struct {
	pool *pgxpool.Pool
}

// NewBranchLock wraps pool as a BranchLock. pool must point at the agents
// schema, the same pool a Store and AuditLog for this service share.
func NewBranchLock(pool *pgxpool.Pool) *BranchLock {
	return &BranchLock{pool: pool}
}

// Acquire moves the existing, previously queued run runID into the
// 'running' state while recording it as the holder of (orgID, repoID,
// branch). runID must already exist (created via Store.CreateRun) — Acquire
// only ever updates a run, it never creates one, since a run's queued row
// is where its budget, sponsor, and grant were already established.
//
// If another run already holds this exact (org, repo, branch) triple, the
// database's partial unique index rejects the update and Acquire reports
// which run holds it.
func (l *BranchLock) Acquire(ctx context.Context, orgID, repoID uuid.UUID, branch string, runID uuid.UUID) error {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return err
	}

	tag, err := l.pool.Exec(ctx,
		`UPDATE agents.agent_runs
		 SET repo_id = $1, branch = $2, state = 'running', started_at = now()
		 WHERE id = $3 AND org_id = $4`,
		repoID, branch, runID, orgID,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			holder, locked, holderErr := l.Holder(ctx, orgID, repoID, branch)
			if holderErr == nil && locked {
				return fmt.Errorf("branch %q locked by run %s", branch, holder)
			}
			return fmt.Errorf("branch %q locked by another run", branch)
		}
		return fmt.Errorf("acquire branch lock: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("run %s not found", runID)
	}
	return nil
}

// Release ends the (orgID, repoID, branch) lock by moving its current
// holder, if any, to 'succeeded'. Once released, the branch is free for a
// later Acquire: with no row left in 'running' for this triple, the
// partial unique index no longer has anything to conflict with.
func (l *BranchLock) Release(ctx context.Context, orgID, repoID uuid.UUID, branch string) error {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return err
	}
	_, err := l.pool.Exec(ctx,
		`UPDATE agents.agent_runs SET state = 'succeeded', ended_at = now()
		 WHERE org_id = $1 AND repo_id = $2 AND branch = $3 AND state = 'running'`,
		orgID, repoID, branch,
	)
	if err != nil {
		return fmt.Errorf("release branch lock: %w", err)
	}
	return nil
}

// Holder reports the run currently holding (orgID, repoID, branch), if
// any. A run that has reached a terminal state is, by construction, never
// in 'running' any more, so Holder naturally reports a lock as released
// once its run ends — there is no separate expiry mechanism to keep in
// sync with the run state machine.
func (l *BranchLock) Holder(ctx context.Context, orgID, repoID uuid.UUID, branch string) (uuid.UUID, bool, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return uuid.Nil, false, err
	}
	var id uuid.UUID
	err := l.pool.QueryRow(ctx,
		`SELECT id FROM agents.agent_runs
		 WHERE org_id = $1 AND repo_id = $2 AND branch = $3 AND state = 'running'`,
		orgID, repoID, branch,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, fmt.Errorf("branch lock holder: %w", err)
	}
	return id, true, nil
}

// CapFunc wraps next so that a ref update targeting a branch a running
// agent run holds is refused before next is even consulted — the same
// gitops.CapFunc type both the git HTTP and SSH transports call, so pushes
// over either transport are rejected identically while a run holds the
// branch. repoID identifies, in this service's own terms, the repository
// next's repo name resolves to; agent-runtime service composition (outside
// this package) is responsible for supplying that resolution.
func (l *BranchLock) CapFunc(repoID uuid.UUID, next gitops.CapFunc) gitops.CapFunc {
	return func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		for _, ref := range refs {
			branch := strings.TrimPrefix(ref, "refs/heads/")
			holderRunID, holderAgentID, locked, err := l.holderAgent(ctx, orgID, repoID, branch)
			if err != nil {
				return fmt.Errorf("check branch lock: %w", err)
			}
			// The branch is free to push, over any transport, unless it is
			// held and the pusher isn't the very agent identity the
			// holding run belongs to — a human push, or a push from a
			// different agent, is rejected identically to any other
			// out-of-scope push.
			if locked && !(s.ActorKind == "agent" && s.ActorID == holderAgentID) {
				return fmt.Errorf("branch %q is locked by run %s", branch, holderRunID)
			}
		}
		if next == nil {
			return nil
		}
		return next(ctx, s, orgID, repo, refs)
	}
}

// holderAgent is Holder plus the agent identity the holding run belongs
// to, which CapFunc needs to tell "this run's own agent pushing its own
// commits" apart from any other actor.
func (l *BranchLock) holderAgent(ctx context.Context, orgID, repoID uuid.UUID, branch string) (runID, agentID uuid.UUID, locked bool, err error) {
	err = l.pool.QueryRow(ctx,
		`SELECT id, agent_id FROM agents.agent_runs
		 WHERE org_id = $1 AND repo_id = $2 AND branch = $3 AND state = 'running'`,
		orgID, repoID, branch,
	).Scan(&runID, &agentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, uuid.Nil, false, nil
		}
		return uuid.Nil, uuid.Nil, false, fmt.Errorf("branch lock holder: %w", err)
	}
	return runID, agentID, true, nil
}
