package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/workspace"
)

// RecordWorkspaceCleanup is called before exec and again when pod termination
// is observed. Trusted UIDs cannot change and observed termination is monotonic.
func (s *Store) RecordWorkspaceCleanup(ctx context.Context, id workspace.Identity) error {
	if err := authz.RequireOrg(ctx, id.OrgID); err != nil {
		return err
	}
	if id.RunID == uuid.Nil || id.NamespaceUID == "" || id.PodUID == "" {
		return fmt.Errorf("workspace identity unavailable")
	}
	body, err := json.Marshal(id)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE agents.agent_runs SET workspace_identity=CASE WHEN COALESCE((workspace_identity->>'TerminationObserved')::boolean,false) THEN workspace_identity ELSE $3 END,
 workspace_cleanup_pending=true WHERE id=$1 AND org_id=$2 AND
 (workspace_identity IS NULL OR workspace_identity-'TerminationObserved'=$3::jsonb-'TerminationObserved')
 AND ((NOT $4 AND state IN ('queued','running') AND NOT execution_finished) OR ($4 AND workspace_identity IS NOT NULL))`, id.RunID, id.OrgID, body, id.TerminationObserved)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("workspace identity conflict or missing run")
	}
	return nil
}

// ConfirmWorkspaceCleanup owns the verification, rather than trusting an
// unrelated session Close or the earlier container-termination milestone.
// Namespace deletion/absence must also succeed before clearing the obligation.
func (s *Store) ConfirmWorkspaceCleanup(ctx context.Context, id uuid.UUID) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	run, err := s.GetRun(ctx, id)
	if err != nil {
		return err
	}
	if !run.WorkspaceCleanupPending {
		return nil
	}
	if s.WorkspaceCleaner == nil || run.WorkspaceIdentity == nil {
		return fmt.Errorf("responsible workspace cleaner unavailable")
	}
	if err := s.WorkspaceCleaner(ctx, *run.WorkspaceIdentity); err != nil {
		return err
	}
	identity, err := json.Marshal(run.WorkspaceIdentity)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE agents.agent_runs SET workspace_cleanup_pending=false,workspace_cleanup_error='' WHERE id=$1 AND org_id=$2 AND COALESCE((workspace_identity->>'TerminationObserved')::boolean,false)
 AND workspace_identity-'TerminationObserved'=$3::jsonb-'TerminationObserved'`, id, scope.OrgID, identity)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("workspace termination evidence unavailable")
	}
	return nil
}
