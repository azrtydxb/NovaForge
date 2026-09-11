package work

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/novaforge/novaforge/internal/authz"
)

// ProposeFinding creates an unassigned, open Work Item for a maintenance
// finding identified by fingerprint, or — if that (orgID, repoID,
// fingerprint) triple has already been proposed — returns the Work Item
// created the first time, unchanged. Both effects happen in one
// transaction, keyed on maintenance_proposals' (org_id, repo_id,
// fingerprint) primary key, so proposing the identical finding twice
// yields one Work Item, not two.
func (s *Store) ProposeFinding(ctx context.Context, orgID, repoID uuid.UUID, fingerprint string, item Item) (Item, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return Item{}, err
	}
	if !validTypes[item.Type] {
		return Item{}, fmt.Errorf("invalid type %q", item.Type)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Item{}, fmt.Errorf("propose finding: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var existingID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT work_item_id FROM work.maintenance_proposals
		WHERE org_id = $1 AND repo_id = $2 AND fingerprint = $3`,
		orgID, repoID, fingerprint,
	).Scan(&existingID)
	switch {
	case err == nil:
		return s.get(ctx, orgID, existingID)
	case errors.Is(err, pgx.ErrNoRows):
		// Not yet proposed; fall through to create it.
	default:
		return Item{}, fmt.Errorf("propose finding: check existing proposal: %w", err)
	}

	item.ID = uuid.New()
	item.OrgID = orgID
	item.RepoID = repoID
	if item.Acceptance == nil {
		item.Acceptance = []string{}
	}
	if item.Constraints == nil {
		item.Constraints = []string{}
	}
	if item.RequiredGates == nil {
		item.RequiredGates = []string{}
	}

	// A proposed Work Item is always created open with NO assignee —
	// autonomous maintenance proposes for approval, it never assigns (and
	// therefore never starts) anything itself.
	err = tx.QueryRow(ctx, `
		INSERT INTO work.work_items (
			id, org_id, repo_id, seq, key, type, goal, acceptance, constraints,
			required_gates, state
		)
		VALUES (
			$1, $2, $3,
			COALESCE((SELECT MAX(seq) FROM work.work_items WHERE org_id = $2), 0) + 1,
			'NF-' || (COALESCE((SELECT MAX(seq) FROM work.work_items WHERE org_id = $2), 0) + 1),
			$4, $5, $6, $7, $8, 'open'
		)
		RETURNING key, state, created_at`,
		item.ID, item.OrgID, item.RepoID,
		item.Type, item.Goal, item.Acceptance, item.Constraints, item.RequiredGates,
	).Scan(&item.Key, &item.State, &item.CreatedAt)
	if err != nil {
		return Item{}, fmt.Errorf("propose finding: insert work item: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO work.maintenance_proposals (org_id, repo_id, fingerprint, work_item_id)
		VALUES ($1, $2, $3, $4)`,
		orgID, repoID, fingerprint, item.ID,
	); err != nil {
		return Item{}, fmt.Errorf("propose finding: record fingerprint: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Item{}, fmt.Errorf("propose finding: commit: %w", err)
	}
	return item, nil
}

// OpenProposalFingerprints returns every unresolved maintenance proposal
// for (orgID, repoID) as fingerprint -> work item id, scoped to the
// caller's org.
func (s *Store) OpenProposalFingerprints(ctx context.Context, orgID, repoID uuid.UUID) (map[string]uuid.UUID, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT fingerprint, work_item_id FROM work.maintenance_proposals
		WHERE org_id = $1 AND repo_id = $2 AND resolved_at IS NULL`,
		orgID, repoID,
	)
	if err != nil {
		return nil, fmt.Errorf("open proposal fingerprints: %w", err)
	}
	defer rows.Close()

	out := make(map[string]uuid.UUID)
	for rows.Next() {
		var fp string
		var id uuid.UUID
		if err := rows.Scan(&fp, &id); err != nil {
			return nil, fmt.Errorf("scan proposal: %w", err)
		}
		out[fp] = id
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("open proposal fingerprints: %w", err)
	}
	return out, nil
}

// ResolveProposal marks the maintenance proposal for (orgID, repoID,
// fingerprint) resolved and moves its Work Item to state "done", appending
// note to its Goal — the finding it was raised for no longer reproduces,
// so the proposal is closed with an explanation rather than deleted, and
// rather than left open forever. A fingerprint that is already resolved,
// or was never proposed, is a silent no-op: closing it again is not an
// error.
func (s *Store) ResolveProposal(ctx context.Context, orgID, repoID uuid.UUID, fingerprint, note string) error {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("resolve proposal: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var workItemID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT work_item_id FROM work.maintenance_proposals
		WHERE org_id = $1 AND repo_id = $2 AND fingerprint = $3 AND resolved_at IS NULL
		FOR UPDATE`,
		orgID, repoID, fingerprint,
	).Scan(&workItemID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("resolve proposal: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE work.maintenance_proposals SET resolved_at = now()
		WHERE org_id = $1 AND repo_id = $2 AND fingerprint = $3`,
		orgID, repoID, fingerprint,
	); err != nil {
		return fmt.Errorf("resolve proposal: mark resolved: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE work.work_items
		SET state = 'done', goal = goal || $2
		WHERE id = $1`,
		workItemID, fmt.Sprintf("\n\n[resolved automatically] %s", note),
	); err != nil {
		return fmt.Errorf("resolve proposal: close work item: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("resolve proposal: commit: %w", err)
	}
	return nil
}
