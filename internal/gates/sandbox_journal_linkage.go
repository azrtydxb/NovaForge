package gates

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Attempt-wide execution profile. Tools/argv/containers/pods are intentionally
// invocation-specific; multiple tools cannot change the evaluated revision or
// silently select another operator target/image within one gate attempt.
type sandboxAttemptIdentity struct {
	OrgID          uuid.UUID
	AttemptID      uuid.UUID
	RunID          uuid.UUID
	RepoID         uuid.UUID
	Gate           string
	SourceSHA      string
	PolicySHA      string
	SnapshotDigest string
	ImageDigest    string
	Target         string
	Namespace      string
}

func ensureSandboxAttempt(ctx context.Context, tx pgx.Tx, id SandboxIdentity) error {
	want := sandboxAttemptIdentity{OrgID: id.OrgID, AttemptID: id.AttemptID, RunID: id.RunID, RepoID: id.RepoID, Gate: id.Gate, SourceSHA: id.SourceSHA, PolicySHA: id.PolicySHA, SnapshotDigest: id.SnapshotDigest, ImageDigest: id.ImageDigest, Target: id.Target, Namespace: id.Namespace}
	_, err := tx.Exec(ctx, `INSERT INTO gates.sandbox_attempts (id,org_id,identity) VALUES ($1,$2,$3) ON CONFLICT (id) DO NOTHING`, id.AttemptID, id.OrgID, want)
	if err != nil {
		return err
	}
	var got sandboxAttemptIdentity
	err = tx.QueryRow(ctx, `SELECT identity FROM gates.sandbox_attempts WHERE org_id=$1 AND id=$2`, id.OrgID, id.AttemptID).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSandboxConflict
	}
	if err != nil {
		return err
	}
	if got != want {
		return ErrSandboxConflict
	}
	return nil
}

// The unique evaluation ID serializes competing links even across targets.
// A foreign collision is rejected without ever reading the foreign tenant row.
func ensureSandboxEvaluation(ctx context.Context, tx pgx.Tx, id SandboxIdentity, evaluation uuid.UUID) error {
	_, err := tx.Exec(ctx, `INSERT INTO gates.sandbox_evaluation_links (evaluation_id,org_id,attempt_id) VALUES ($1,$2,$3) ON CONFLICT (evaluation_id) DO NOTHING`, evaluation, id.OrgID, id.AttemptID)
	if err != nil {
		return err
	}
	var attempt uuid.UUID
	err = tx.QueryRow(ctx, `SELECT attempt_id FROM gates.sandbox_evaluation_links WHERE org_id=$1 AND evaluation_id=$2`, id.OrgID, evaluation).Scan(&attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSandboxConflict
	}
	if err != nil {
		return err
	}
	if attempt != id.AttemptID {
		return ErrSandboxConflict
	}
	return nil
}
