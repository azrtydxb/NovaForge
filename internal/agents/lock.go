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

	// Only a queued run may take the lock. A run cancelled between StartRun
	// returning and this update must stay cancelled, not be revived into
	// "running" — and holding the branch — by a start that lost the race.
	tag, err := l.pool.Exec(ctx,
		`UPDATE agents.agent_runs
		 SET repo_id = $1, branch = $2, state = 'running', started_at = now()
		 WHERE id = $3 AND org_id = $4 AND state = 'queued'`,
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
		return fmt.Errorf("run %s not found, or no longer queued", runID)
	}
	return nil
}

// BranchNamespace is where every agent branch lives: StartRun issues each
// run a grant for BranchNamespace + "<work item key>/". The git transports
// consult agent-runtime about a push only for refs in this namespace, so a
// push anywhere else never depends on agent-runtime being reachable.
const BranchNamespace = "agents/"

// LockPrefix is the part of the branch namespace a run holding branch locks:
// its grant's prefix ("agents/NF-1/" for "agents/NF-1/work"), not just the one
// branch. The agent may write anywhere under its prefix, so a person pushing
// to "agents/NF-1/other" is interfering with the same work.
func LockPrefix(branch string) string {
	if strings.HasSuffix(branch, "/"+runBranchLeaf) {
		return strings.TrimSuffix(branch, runBranchLeaf)
	}
	return branch + "/"
}

// Holding describes the run that holds a ref.
type Holding struct {
	RunID   uuid.UUID
	AgentID uuid.UUID
	Prefix  string
}

// Check reports the running run, in the caller's organization, whose lock
// covers ref in repoID. ref may carry "refs/heads/". A run holds its lock
// exactly while it is "running", so a run that has ended — however it ended —
// holds nothing, with no separate release to forget.
func (l *BranchLock) Check(ctx context.Context, repoID uuid.UUID, ref string) (Holding, bool, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return Holding{}, false, err
	}
	branch := strings.TrimPrefix(ref, "refs/heads/")
	rows, err := l.pool.Query(ctx,
		`SELECT id, agent_id, branch FROM agents.agent_runs
		 WHERE org_id = $1 AND repo_id = $2 AND state = 'running'`,
		scope.OrgID, repoID,
	)
	if err != nil {
		return Holding{}, false, fmt.Errorf("check branch lock: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var h Holding
		var held string
		if err := rows.Scan(&h.RunID, &h.AgentID, &held); err != nil {
			return Holding{}, false, fmt.Errorf("scan branch lock: %w", err)
		}
		if held == "" {
			continue
		}
		h.Prefix = LockPrefix(held)
		if branch == held || strings.HasPrefix(branch, h.Prefix) {
			return h, true, nil
		}
	}
	return Holding{}, false, rows.Err()
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

// RefuseWrite decides whether actor may write ref while h holds it. Only the
// holding run's own agent may: a person, a service, or any other agent is
// refused, whatever write access they would otherwise have. It is the one
// rule both git transports (through git-platform) and CapFunc apply, so they
// cannot disagree about who a lock admits.
func RefuseWrite(actor authz.Scope, ref string, h Holding) error {
	if actor.ActorKind == "agent" && actor.ActorID == h.AgentID {
		return nil
	}
	return fmt.Errorf("branch %q is locked by Agent Run %s, which is working under %s; it can be written again when that run ends",
		strings.TrimPrefix(ref, "refs/heads/"), h.RunID, h.Prefix)
}

// CapFunc wraps next so that a ref update under a prefix a running agent run
// holds is refused before next is even consulted — the same gitops.CapFunc
// type both the git HTTP and SSH transports call. It serves a caller that can
// read this schema directly; git-platform cannot, and asks agent-runtime
// through the CheckBranchLock RPC, which answers from Check.
func (l *BranchLock) CapFunc(repoID uuid.UUID, next gitops.CapFunc) gitops.CapFunc {
	return func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		if err := authz.RequireOrg(ctx, orgID); err != nil {
			return err
		}
		for _, ref := range refs {
			h, locked, err := l.Check(ctx, repoID, ref)
			if err != nil {
				return err
			}
			if locked {
				if err := RefuseWrite(s, ref, h); err != nil {
					return err
				}
			}
		}
		if next == nil {
			return nil
		}
		return next(ctx, s, orgID, repo, refs)
	}
}
