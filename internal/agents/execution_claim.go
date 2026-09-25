package agents

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"google.golang.org/protobuf/proto"
)

// ExecutionClaim is a stable admission identity, never a new identity on retry.
type ExecutionClaim struct{ WorkItemID, RepoID, RunID, AgentID uuid.UUID }
type ExecutionRelease struct {
	ExecutionClaim
	Outcome string
}

// ExecutionClaims is the authenticated Work-owned protocol. Release of an
// absent claim with admission_failed must fence delayed Claim of this identity.
// The adapter must authenticate the exact authorized agent-runtime service;
// local authz scope or stored issuer identifiers are not outbound credentials.
type ExecutionClaims interface {
	ClaimExecution(context.Context, ExecutionClaim) (*workv1.WorkItem, error)
	ReleaseExecution(context.Context, ExecutionRelease) error
}

func (r Run) ExecutionClaim() ExecutionClaim {
	return ExecutionClaim{r.WorkItemID, r.RepoID, r.ID, r.AgentID}
}

func (s *Store) FreezeWorkItem(ctx context.Context, run Run, item *workv1.WorkItem) error {
	if item == nil || item.GetId() != run.WorkItemID.String() || item.GetRepoId() != run.RepoID.String() {
		return fmt.Errorf("work claim returned mismatched intent")
	}
	if err := authz.RequireOrg(ctx, run.OrgID); err != nil {
		return err
	}
	body, err := proto.Marshal(item)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE agents.agent_runs SET work_item_snapshot=$3 WHERE id=$1 AND org_id=$2 AND state='queued' AND (work_item_snapshot IS NULL OR work_item_snapshot=$3)`, run.ID, run.OrgID, body)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("admission no longer owns queued run")
	}
	return nil
}

// FrozenWorkItem refuses legacy admitted runs with no immutable intent. Loop
// fixtures can omit WorkClaimRequired, but StartRun always sets it.
func (r Run) FrozenWorkItem() (*workv1.WorkItem, error) {
	if len(r.WorkItemSnapshot) == 0 {
		return nil, fmt.Errorf("run has no frozen Work execution intent")
	}
	item := new(workv1.WorkItem)
	if err := proto.Unmarshal(r.WorkItemSnapshot, item); err != nil {
		return nil, err
	}
	if item.GetId() != r.WorkItemID.String() || item.GetRepoId() != r.RepoID.String() {
		return nil, fmt.Errorf("frozen Work execution identity mismatch")
	}
	return item, nil
}
