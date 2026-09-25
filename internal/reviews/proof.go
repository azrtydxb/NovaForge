package reviews

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/gatenames"
)

var ErrProofAuthority = errors.New("proof producer is not authorized for this namespace")

// proofProducer uses only authenticated identity. A gate-shaped human assertion
// is not gate evidence, even if its status text happens to say "pass".
func proofProducer(ctx context.Context, gate string) (string, uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return "", uuid.Nil, ErrProofAuthority
	}
	if scope.ActorKind == "user" && scope.ActorID != uuid.Nil {
		prefix := "assertion/" + scope.ActorID.String() + "/"
		label, ok := strings.CutPrefix(gate, prefix)
		if ok && len(label) > 0 && len(label) <= 128 && !strings.ContainsAny(label, "/\\\r\n\t") && strings.TrimSpace(label) == label {
			return "human", scope.ActorID, nil
		}
	}
	if scope.ActorKind == "service" {
		switch scope.ServiceName {
		case "gates":
			for _, name := range gatenames.All() {
				if gate == name {
					return "gates", uuid.Nil, nil
				}
			}
			switch gate {
			case "approval/read_source", "approval/modify_workspace", "approval/add_dependency", "approval/change_db_schema", "approval/access_secret", "approval/deploy_staging", "approval/deploy_production", "approval/change_gate_config", "architecture-decision":
				return "gates", uuid.Nil, nil
			}
		case "work-reviews":
			for _, role := range DefaultReviewRoles {
				if gate == "review:"+role {
					return "work-reviews", uuid.Nil, nil
				}
			}
		case "agent-runtime":
			if gate == "agent-blocked" {
				return "agent-runtime", uuid.Nil, nil
			}
		}
	}
	return "", uuid.Nil, ErrProofAuthority
}

// RecordProof keeps an append-only audit alongside the current display row.
// Replacing a legacy row cannot erase its unverified history. Neither caller
// supplied provenance nor a service from a different namespace can overwrite it.
func (s *Store) RecordProof(ctx context.Context, runID uuid.UUID, gate, state, detail string) error {
	producer, actor, err := proofProducer(ctx, gate)
	if err != nil {
		return err
	}
	scope, _ := authz.FromContext(ctx)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO reviews.run_proof(run_id,gate,status,detail,producer,actor_id)
 SELECT id,$3,$4,$5,$6,$7 FROM reviews.runs WHERE id=$1 AND org_id=$2
 ON CONFLICT(run_id,gate) DO UPDATE SET status=EXCLUDED.status,detail=EXCLUDED.detail,producer=EXCLUDED.producer,actor_id=EXCLUDED.actor_id,recorded_at=now()
 WHERE run_proof.producer='' OR (run_proof.producer=EXCLUDED.producer AND run_proof.actor_id=EXCLUDED.actor_id)`, runID, scope.OrgID, gate, state, detail, producer, actor)
	if err != nil {
		return fmt.Errorf("record proof: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrProofAuthority
	}
	_, err = tx.Exec(ctx, `INSERT INTO reviews.proof_audit(run_id,gate,status,detail,producer,actor_id,recorded_at) SELECT p.run_id,p.gate,p.status,p.detail,p.producer,p.actor_id,p.recorded_at FROM reviews.run_proof p JOIN reviews.runs r ON r.id=p.run_id WHERE p.run_id=$1 AND r.org_id=$2 AND p.gate=$3`, runID, scope.OrgID, gate)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
