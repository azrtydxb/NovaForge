package agents

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Admission and deletion serialize on one organization key. Only the short
// local transaction holds it; no remote cleanup runs under a database lock.
func lockAdmission(ctx context.Context, tx pgx.Tx, org uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "agents/admission/"+org.String())
	return err
}
func allowAdmission(ctx context.Context, tx pgx.Tx, org, repo uuid.UUID) error {
	if err := lockAdmission(ctx, tx, org); err != nil {
		return err
	}
	var deleted bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agents.deleted_scopes WHERE org_id=$1 AND repo_id IN ($2,$3))`, org, repo, uuid.Nil).Scan(&deleted); err != nil {
		return err
	}
	if deleted {
		return fmt.Errorf("run scope is being deleted")
	}
	return nil
}
