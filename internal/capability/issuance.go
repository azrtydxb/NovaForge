package capability

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/novaforge/novaforge/internal/authz"
)

// IssuanceIntent binds one immutable run grant to its authenticated issuer.
// No credential is stored. Background cleanup needs explicit owner-verified
// delegation to this issuer; copying these fields is not authentication.
type IssuanceIntent struct {
	IssuerID   uuid.UUID
	IssuerKind string
	RunID      uuid.UUID
	Grant      Grant
}

var ErrIssuanceDenied = errors.New("grant issuance denied")

// issuanceTransaction admits only the same authenticated issuer in the same
// organization, never an arbitrary service bypass. The owner RPC must separately
// validate subject membership and any delegated background cleanup authority.
func (s *Store) issuanceTransaction(ctx context.Context, i IssuanceIntent) (pgx.Tx, []byte, error) {
	scope, err := authz.FromContext(ctx)
	g := i.Grant
	if err != nil || scope.OrgID == uuid.Nil || scope.OrgID != g.OrgID || scope.ActorID == uuid.Nil || scope.ActorID != i.IssuerID || scope.ActorKind != i.IssuerKind || i.RunID == uuid.Nil || g.ID == uuid.Nil || g.SubjectID == uuid.Nil || g.SubjectKind != "agent" || !g.RepoRead || g.SecretsProd || g.DeployProd || g.DeployStaging || !strings.HasPrefix(g.WriteBranch, "agents/") || !strings.HasSuffix(g.WriteBranch, "/") || strings.Contains(g.WriteBranch, "..") {
		return nil, nil, ErrIssuanceDenied
	}
	body, err := json.Marshal(i)
	if err != nil {
		return nil, nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "capability/issuance/"+g.ID.String()); err != nil {
		tx.Rollback(ctx)
		return nil, nil, err
	}
	var identical bool
	err = tx.QueryRow(ctx, `SELECT intent=$2::jsonb FROM gitplatform.capability_issuances WHERE id=$1`, g.ID, body).Scan(&identical)
	if err == nil && !identical {
		tx.Rollback(ctx)
		return nil, nil, ErrIssuanceDenied
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		tx.Rollback(ctx)
		return nil, nil, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// An unrelated legacy grant with this ID cannot be adopted or cancelled.
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gitplatform.capability_grants WHERE id=$1)`, g.ID).Scan(&exists); err != nil || exists {
			tx.Rollback(ctx)
			return nil, nil, ErrIssuanceDenied
		}
		_, err = tx.Exec(ctx, `INSERT INTO gitplatform.capability_issuances(id,org_id,issuer_id,issuer_kind,run_id,intent) VALUES($1,$2,$3,$4,$5,$6)`, g.ID, g.OrgID, i.IssuerID, i.IssuerKind, i.RunID, body)
		if err != nil {
			tx.Rollback(ctx)
			return nil, nil, err
		}
	}
	return tx, body, nil
}

// IssueIntent is the run-issuance path; generic Issue keeps its legacy API.
// An identical retry returns the original expiry, never renews authority.
func (s *Store) IssueIntent(ctx context.Context, i IssuanceIntent) (Grant, error) {
	tx, _, err := s.issuanceTransaction(ctx, i)
	if err != nil {
		return Grant{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var cancelled bool
	if err = tx.QueryRow(ctx, `SELECT cancelled FROM gitplatform.capability_issuances WHERE id=$1`, i.Grant.ID).Scan(&cancelled); err != nil {
		return Grant{}, err
	}
	if cancelled {
		return Grant{}, ErrIssuanceDenied
	}
	g := i.Grant
	// Use the database's clock, including for replay: a revoked/expired grant
	// cannot be reconstructed from an otherwise identical immutable receipt.
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT $1::timestamptz>now() AND $1::timestamptz<=now()+interval '24 hours'`, g.ExpiresAt).Scan(&valid); err != nil || !valid {
		return Grant{}, ErrIssuanceDenied
	}
	var live bool
	err = tx.QueryRow(ctx, `SELECT expires_at>now() FROM gitplatform.capability_grants WHERE id=$1 FOR UPDATE`, g.ID).Scan(&live)
	if err == nil {
		if !live {
			return Grant{}, ErrIssuanceDenied
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO gitplatform.capability_grants(id,org_id,subject_id,subject_kind,repo_read,write_branch,secrets_prod,deploy_staging,deploy_prod,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, g.ID, g.OrgID, g.SubjectID, g.SubjectKind, g.RepoRead, g.WriteBranch, g.SecretsProd, g.DeployStaging, g.DeployProd, g.ExpiresAt)
	}
	if err != nil {
		return Grant{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Grant{}, err
	}
	return g, nil
}

// CancelIssuance durably fences even a not-yet-issued ID. Unlike Revoke, this
// requires the full authorized intent and prevents ALL later IssueIntent calls.
// Tombstones have no TTL: request replay must never resurrect authority.
func (s *Store) CancelIssuance(ctx context.Context, i IssuanceIntent) error {
	tx, _, err := s.issuanceTransaction(ctx, i)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `UPDATE gitplatform.capability_issuances SET cancelled=true WHERE id=$1`, i.Grant.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE gitplatform.capability_grants SET expires_at=LEAST(expires_at,'epoch'::timestamptz) WHERE id=$1 AND org_id=$2`, i.Grant.ID, i.Grant.OrgID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
