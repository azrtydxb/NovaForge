package agents

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
)

// Completion is an immutable execution receipt. Retrying exactly the same
// receipt after an ambiguous commit is safe; a different receipt is a conflict.
// Availability describes the whole execution, not whether a partial count is
// known. Known partial counters are retained even when availability is false.
type Completion struct {
	State   string
	Spend   Spend
	Summary string
}

// CompleteRun commits accounting, outcome, summary evidence and cleanup intent
// together. Earlier cancellation/recovery wins the state race, but must not
// discard the executing replica's measured receipt. No owner RPC is in this tx.
func (s *Store) CompleteRun(ctx context.Context, id uuid.UUID, c Completion) (string, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return "", err
	}
	if !terminalStates[c.State] || c.Spend.Tokens < 0 || c.Spend.CostMicros < 0 {
		return "", fmt.Errorf("invalid completion")
	}
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var state string
	var recorded, identical, workspacePending bool
	err = tx.QueryRow(ctx, `SELECT state,completion IS NOT NULL,COALESCE(completion=$3::jsonb,false),workspace_cleanup_pending FROM agents.agent_runs WHERE id=$1 AND org_id=$2 FOR UPDATE`, id, scope.OrgID, body).Scan(&state, &recorded, &identical, &workspacePending)
	if err != nil {
		return "", err
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agents.tool_calls WHERE org_id=$1 AND run_id=$2 AND outcome IN ('pending','unavailable'))`, scope.OrgID, id).Scan(&pending); err != nil {
		return state, err
	}
	if c.State == "succeeded" && pending {
		return state, fmt.Errorf("tool evidence unresolved; success refused")
	}
	if c.State == "succeeded" && workspacePending {
		return state, fmt.Errorf("workspace teardown unconfirmed; success unresolved")
	}
	if recorded {
		if !identical {
			return state, fmt.Errorf("conflicting completion receipt")
		}
		return state, tx.Commit(ctx)
	}
	if terminalStates[state] {
		if state != "cancelled" && state != "failed" {
			return state, fmt.Errorf("stale completion for terminal run")
		}
	} else {
		state = c.State
	}
	_, err = tx.Exec(ctx, `UPDATE agents.agent_runs SET execution_finished=true,state=$3,ended_at=COALESCE(ended_at,now()),
 tokens_used=GREATEST(tokens_used,$4),cost_used_micros=GREATEST(cost_used_micros,$5),
 tokens_available=$6,cost_available=$7,end_reason=CASE WHEN state IN ('cancelled','failed') THEN end_reason ELSE $8 END,
 completion=$9,grant_cleanup_pending=grant_cleanup_pending OR (ended_at IS NULL AND grant_id <> '00000000-0000-0000-0000-000000000000')
 WHERE id=$1 AND org_id=$2`, id, scope.OrgID, state, c.Spend.Tokens, c.Spend.CostMicros, c.Spend.TokensAvailable, c.Spend.CostAvailable, c.Spend.Reason, body)
	if err != nil {
		return "", err
	}
	evidence, _ := json.Marshal(map[string]any{"state": state, "summary": c.Summary, "tokens_available": c.Spend.TokensAvailable, "cost_available": c.Spend.CostAvailable})
	_, err = tx.Exec(ctx, `INSERT INTO agents.tool_calls (id,run_id,org_id,tool,args_json,outcome,ended_at) VALUES ($1,$2,$3,'run.summary',$4,'ok',now())`, uuid.New(), id, scope.OrgID, evidence)
	if err != nil {
		return "", err
	}
	return state, tx.Commit(ctx)
}
