package agents

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/authz"
)

func purgeOrg(ctx context.Context) (uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return uuid.Nil, errors.New("purge requires an organization scope")
	}
	return scope.OrgID, nil
}

// RunIDsToPurge returns the Agent Runs a purge would delete: a repository's
// when repoID is set, else every run of the caller's organization.
func (s *Store) RunIDsToPurge(ctx context.Context, repoID *uuid.UUID) ([]uuid.UUID, error) {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM agents.agent_runs WHERE org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2)`, orgID, repoID)
	if err != nil {
		return nil, fmt.Errorf("list agent runs to purge: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PurgeRuns deletes Agent Runs — with their provenance and tool calls — of a
// repository (repoID set) or of the whole organization in scope.
//
// A run still going is durably cancelled and its grant revoked before its row is
// removed. The loop stops on seeing its run cancelled, and also on finding the
// row gone (see agentrun's cancellation check): left running, it would carry
// on calling tools against a repository that no longer exists.
func (s *Store) PurgeRuns(ctx context.Context, repoID *uuid.UUID) (int64, error) {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return 0, err
	}
	// Mark deletion before inspecting rows. A late StartRun cannot slip past
	// the final check, or resurrect scope after this purge returns.
	fence, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer fence.Rollback(context.WithoutCancel(ctx))
	if err = lockAdmission(ctx, fence, orgID); err != nil {
		return 0, err
	}
	key := uuid.Nil
	if repoID != nil {
		key = *repoID
	}
	if _, err = fence.Exec(ctx, `INSERT INTO agents.deleted_scopes(org_id,repo_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, orgID, key); err != nil {
		return 0, err
	}
	if err = fence.Commit(ctx); err != nil {
		return 0, err
	}
	// Persist cancellation and cleanup intent before contacting the grant
	// owner. A failed revoke must leave evidence for retry, not delete it.
	if _, err := s.pool.Exec(ctx, `UPDATE agents.agent_runs SET state='cancelled', ended_at=now(),
        grant_cleanup_pending=grant_id <> '00000000-0000-0000-0000-000000000000'
        WHERE org_id=$1 AND ($2::uuid IS NULL OR repo_id=$2) AND state IN ('queued','running')`, orgID, repoID); err != nil {
		return 0, err
	}
	ids, err := s.RunIDsToPurge(ctx, repoID)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := s.CleanupRunGrant(ctx, id); err != nil {
			return 0, err
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("purge agent runs: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	// Lock the selected rows and recheck: a new run that arrived while remote
	// cleanup was in progress is not part of the confirmed set.
	rows, err := tx.Query(ctx, `SELECT id,state,(grant_cleanup_pending OR work_release_pending OR workspace_cleanup_pending) FROM agents.agent_runs WHERE org_id=$1 AND ($2::uuid IS NULL OR repo_id=$2) FOR UPDATE`, orgID, repoID)
	if err != nil {
		return 0, err
	}
	var confirmed []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		var state string
		var pending bool
		if err := rows.Scan(&id, &state, &pending); err != nil {
			rows.Close()
			return 0, err
		}
		if !terminalStates[state] || pending {
			rows.Close()
			return 0, fmt.Errorf("run cleanup pending; purge must retry")
		}
		confirmed = append(confirmed, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM agents.tool_calls WHERE org_id = $1 AND run_id = ANY($2::uuid[])`,
		orgID, confirmed); err != nil {
		return 0, fmt.Errorf("purge tool calls: %w", err)
	}
	tag, err := tx.Exec(ctx,
		`DELETE FROM agents.agent_runs WHERE org_id = $1 AND id = ANY($2::uuid[])`, orgID, confirmed)
	if err != nil {
		return 0, fmt.Errorf("purge agent runs: %w", err)
	}
	var remaining bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agents.agent_runs WHERE org_id=$1 AND ($2::uuid IS NULL OR repo_id=$2))`, orgID, repoID).Scan(&remaining); err != nil {
		return 0, err
	}
	if remaining {
		return 0, fmt.Errorf("new run arrived during purge; retry required")
	}
	if repoID == nil {
		if _, err := tx.Exec(ctx, `DELETE FROM agents.agents WHERE org_id = $1`, orgID); err != nil {
			return 0, fmt.Errorf("purge agents: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("purge agent runs: %w", err)
	}
	return tag.RowsAffected(), nil
}
