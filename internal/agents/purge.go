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
// A run still going is cancelled in the same transaction before its row is
// removed. The loop stops on seeing its run cancelled, and also on finding the
// row gone (see agentrun's cancellation check): left running, it would carry
// on calling tools against a repository that no longer exists.
func (s *Store) PurgeRuns(ctx context.Context, repoID *uuid.UUID) (int64, error) {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("purge agent runs: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if _, err := tx.Exec(ctx, `
		UPDATE agents.agent_runs SET state = 'cancelled', ended_at = now()
		WHERE org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2) AND state IN ('queued', 'running')`,
		orgID, repoID); err != nil {
		return 0, fmt.Errorf("cancel agent runs before purge: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM agents.tool_calls WHERE org_id = $1 AND run_id IN (
			SELECT id FROM agents.agent_runs WHERE org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2))`,
		orgID, repoID); err != nil {
		return 0, fmt.Errorf("purge tool calls: %w", err)
	}
	tag, err := tx.Exec(ctx,
		`DELETE FROM agents.agent_runs WHERE org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2)`, orgID, repoID)
	if err != nil {
		return 0, fmt.Errorf("purge agent runs: %w", err)
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
