package capability

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/novaforge/novaforge/internal/authz"
	"strings"
)

// LegacyBinding is cancellation evidence, not an issuance receipt. Identity
// obtains it from the authenticated run owner, never from request fields.
type LegacyBinding struct {
	GrantID, OrgID, RunID, AgentID uuid.UUID
	Branch                         string
}

func (s *Store) LegacyCancellation(ctx context.Context, id uuid.UUID) (LegacyBinding, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return LegacyBinding{}, err
	}
	if err = authz.RequireOrg(ctx, scope.OrgID); err != nil {
		return LegacyBinding{}, err
	}
	b := LegacyBinding{GrantID: id, OrgID: scope.OrgID}
	err = s.pool.QueryRow(ctx, `SELECT run_id,agent_id,branch FROM gitplatform.legacy_grant_cancellations WHERE id=$1 AND org_id=$2`, id, scope.OrgID).Scan(&b.RunID, &b.AgentID, &b.Branch)
	return b, err
}
func (s *Store) CancelVerifiedLegacyGrant(ctx context.Context, b LegacyBinding) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || scope.OrgID != b.OrgID || scope.ActorKind != "service" || scope.ServiceName != "agent-runtime" || b.GrantID == uuid.Nil || b.RunID == uuid.Nil || b.AgentID == uuid.Nil || b.Branch == "" {
		return ErrIssuanceDenied
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "capability/issuance/"+b.GrantID.String()); err != nil {
		return err
	}
	var identical bool
	err = tx.QueryRow(ctx, `SELECT org_id=$2 AND run_id=$3 AND agent_id=$4 AND branch=$5 FROM gitplatform.legacy_grant_cancellations WHERE id=$1`, b.GrantID, b.OrgID, b.RunID, b.AgentID, b.Branch).Scan(&identical)
	if err == nil {
		if !identical {
			return ErrIssuanceDenied
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var agent uuid.UUID
	var kind, branch string
	err = tx.QueryRow(ctx, `SELECT subject_id,subject_kind,write_branch FROM gitplatform.capability_grants WHERE id=$1 AND org_id=$2 FOR UPDATE`, b.GrantID, b.OrgID).Scan(&agent, &kind, &branch)
	if err != nil {
		return err
	}
	expected := branch
	if strings.HasSuffix(branch, "/") {
		expected += "work"
	}
	if agent != b.AgentID || kind != "agent" || branch == "" || expected != b.Branch {
		return ErrIssuanceDenied
	}
	var issued bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gitplatform.capability_issuances WHERE id=$1)`, b.GrantID).Scan(&issued); err != nil {
		return err
	}
	if issued {
		return ErrIssuanceDenied
	}
	if _, err = tx.Exec(ctx, `INSERT INTO gitplatform.legacy_grant_cancellations(id,org_id,run_id,agent_id,branch) VALUES($1,$2,$3,$4,$5)`, b.GrantID, b.OrgID, b.RunID, b.AgentID, b.Branch); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE gitplatform.capability_grants SET expires_at=LEAST(expires_at,'epoch'::timestamptz) WHERE id=$1 AND org_id=$2`, b.GrantID, b.OrgID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
