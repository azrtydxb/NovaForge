package work

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
// caller's org. A dismissed proposal is not open: a person closed it with a
// reason, and a later scan closing it again would rewrite that record.
func (s *Store) OpenProposalFingerprints(ctx context.Context, orgID, repoID uuid.UUID) (map[string]uuid.UUID, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT fingerprint, work_item_id FROM work.maintenance_proposals
		WHERE org_id = $1 AND repo_id = $2 AND resolved_at IS NULL
		  AND decision IS DISTINCT FROM 'dismissed'`,
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
		  AND decision IS DISTINCT FROM 'dismissed'
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

	// An active execution owns its lifecycle. Leave the proposal unresolved
	// for a later sweep rather than completing it behind the runtime's back.
	tag, err := tx.Exec(ctx, `
		UPDATE work.work_items SET state = 'done', goal = goal || $2
		WHERE id = $1 AND org_id = $3 AND active_execution_run_id IS NULL`,
		workItemID, fmt.Sprintf("\n\n[resolved automatically] %s", note), orgID)
	if err != nil {
		return fmt.Errorf("resolve proposal: close work item: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("resolve proposal: work has active execution")
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("resolve proposal: commit: %w", err)
	}
	return nil
}

// ErrProposalNotFound is returned for a fingerprint the caller's organization
// never had proposed in that repository.
var ErrProposalNotFound = errors.New("maintenance proposal not found")

// ErrProposalDecided is returned when a proposal already carries a decision,
// or its finding has stopped reproducing: a decision is made once.
var ErrProposalDecided = errors.New("maintenance proposal is no longer awaiting a decision")

// ErrNotAPerson is returned when anyone but a person tries to decide a
// proposal. An agent approving work in order to do it is exactly the
// unapproved execution the approval exists to prevent, and a platform worker
// is nobody to answer for the decision.
var ErrNotAPerson = errors.New("only a person may approve or dismiss a maintenance proposal")

// AwaitingApproval reports whether workItemID is a maintenance proposal no
// person has decided yet, within the caller's organization.
func (s *Store) AwaitingApproval(ctx context.Context, workItemID uuid.UUID) (bool, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return false, err
	}
	var awaiting bool
	err = s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM work.maintenance_proposals
			WHERE org_id = $1 AND work_item_id = $2
			  AND decision IS NULL AND resolved_at IS NULL
		)`, scope.OrgID, workItemID).Scan(&awaiting)
	if err != nil {
		return false, fmt.Errorf("check proposal approval: %w", err)
	}
	return awaiting, nil
}

// ApproveProposal records the calling person's approval of the proposal for
// (repoID, fingerprint) and assigns its Work Item — to assigneeID of
// assigneeKind, or to the approver when assigneeID is nil. The item stays
// open: approval makes it actionable, it does not start anything.
func (s *Store) ApproveProposal(ctx context.Context, repoID uuid.UUID, fingerprint string, assigneeID uuid.UUID, assigneeKind string) (Proposal, error) {
	scope, err := decider(ctx)
	if err != nil {
		return Proposal{}, err
	}
	if assigneeID == uuid.Nil {
		assigneeID, assigneeKind = scope.ActorID, "user"
	}
	if assigneeKind != "user" && assigneeKind != "agent" {
		return Proposal{}, fmt.Errorf("invalid assignee kind %q", assigneeKind)
	}

	return s.decide(ctx, scope, repoID, fingerprint, func(tx pgx.Tx, workItemID uuid.UUID) error {
		if _, err := tx.Exec(ctx, `
			UPDATE work.maintenance_proposals
			SET decision = 'approved', decided_by = $1, decided_at = now()
			WHERE org_id = $2 AND repo_id = $3 AND fingerprint = $4`,
			scope.ActorID, scope.OrgID, repoID, fingerprint); err != nil {
			return fmt.Errorf("record approval: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE work.work_items SET assignee_id = $1, assignee_kind = $2
			WHERE org_id = $3 AND id = $4`,
			assigneeID, assigneeKind, scope.OrgID, workItemID); err != nil {
			return fmt.Errorf("assign approved work item: %w", err)
		}
		return nil
	})
}

// DismissProposal records the calling person's dismissal of the proposal for
// (repoID, fingerprint) with reason, closes its Work Item, and puts the
// reason on the item's discussion, where its readers will look for it.
func (s *Store) DismissProposal(ctx context.Context, repoID uuid.UUID, fingerprint, reason string) (Proposal, error) {
	scope, err := decider(ctx)
	if err != nil {
		return Proposal{}, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Proposal{}, fmt.Errorf("a dismissal needs a reason")
	}

	return s.decide(ctx, scope, repoID, fingerprint, func(tx pgx.Tx, workItemID uuid.UUID) error {
		if _, err := tx.Exec(ctx, `
			UPDATE work.maintenance_proposals
			SET decision = 'dismissed', decided_by = $1, decided_at = now(), dismiss_reason = $2
			WHERE org_id = $3 AND repo_id = $4 AND fingerprint = $5`,
			scope.ActorID, reason, scope.OrgID, repoID, fingerprint); err != nil {
			return fmt.Errorf("record dismissal: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE work.work_items SET state = 'done' WHERE org_id = $1 AND id = $2`,
			scope.OrgID, workItemID); err != nil {
			return fmt.Errorf("close dismissed work item: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work.work_item_comments (org_id, work_item_id, author_id, author_kind, body)
			VALUES ($1, $2, $3, 'user', $4)`,
			scope.OrgID, workItemID, scope.ActorID, "Maintenance proposal dismissed: "+reason); err != nil {
			return fmt.Errorf("record dismissal reason: %w", err)
		}
		return nil
	})
}

// decider returns the caller's scope when the caller is a person.
func decider(ctx context.Context) (authz.Scope, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return authz.Scope{}, err
	}
	if scope.ActorKind != "user" || scope.ActorID == uuid.Nil {
		return authz.Scope{}, ErrNotAPerson
	}
	return scope, nil
}

// decide locks the undecided proposal for (repoID, fingerprint) in the
// caller's organization, applies apply inside the same transaction, and
// returns the proposal as it now stands. Locking the row is what makes a
// decision single: two people deciding at once serialise, and the second
// finds the first's decision.
func (s *Store) decide(ctx context.Context, scope authz.Scope, repoID uuid.UUID, fingerprint string, apply func(pgx.Tx, uuid.UUID) error) (Proposal, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Proposal{}, fmt.Errorf("decide proposal: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var workItemID uuid.UUID
	var decided, resolved bool
	err = tx.QueryRow(ctx, `
		SELECT work_item_id, decision IS NOT NULL, resolved_at IS NOT NULL
		FROM work.maintenance_proposals
		WHERE org_id = $1 AND repo_id = $2 AND fingerprint = $3
		FOR UPDATE`,
		scope.OrgID, repoID, fingerprint,
	).Scan(&workItemID, &decided, &resolved)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Proposal{}, ErrProposalNotFound
		}
		return Proposal{}, fmt.Errorf("decide proposal: %w", err)
	}
	if decided || resolved {
		return Proposal{}, ErrProposalDecided
	}
	if err := apply(tx, workItemID); err != nil {
		return Proposal{}, err
	}

	p, err := scanProposal(tx.QueryRow(ctx, `
		SELECT `+proposalColumns+`
		FROM work.maintenance_proposals p
		JOIN work.work_items w ON w.id = p.work_item_id
		WHERE p.org_id = $1 AND p.repo_id = $2 AND p.fingerprint = $3`,
		scope.OrgID, repoID, fingerprint))
	if err != nil {
		return Proposal{}, fmt.Errorf("read decided proposal: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Proposal{}, fmt.Errorf("decide proposal: commit: %w", err)
	}
	return p, nil
}
