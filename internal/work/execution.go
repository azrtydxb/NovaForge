package work

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func executionOwner(ctx context.Context) (authz.Scope, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || scope.ActorKind != "service" || scope.ServiceName != "agent-runtime" || scope.PlatformWorker != "" {
		return authz.Scope{}, status.Error(codes.PermissionDenied, "only the org-scoped agent-runtime service may own execution")
	}
	return scope, nil
}
func executionID(field, raw string) (uuid.UUID, error) {
	id, err := parseUUID(field, raw)
	if err == nil && id == uuid.Nil {
		err = status.Errorf(codes.InvalidArgument, "%s must not be nil", field)
	}
	return id, err
}

// ClaimExecution is the admission linearization point, not a lookup of runs in
// another schema. The runtime persists runID first and retries this exact binding
// after an uncertain reply. No timeout may free a claim with possibly live work.
func (g *GRPCServer) ClaimExecution(ctx context.Context, req *workv1.ClaimExecutionRequest) (*workv1.ClaimExecutionResponse, error) {
	scope, err := executionOwner(ctx)
	if err != nil {
		return nil, err
	}
	itemID, err := executionID("work_item_id", req.GetWorkItemId())
	if err != nil {
		return nil, err
	}
	repoID, err := executionID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	runID, err := executionID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	agentID, err := executionID("agent_id", req.GetAgentId())
	if err != nil {
		return nil, err
	}
	sponsorID, err := executionID("sponsor_id", req.GetSponsorId())
	if err != nil {
		return nil, err
	}
	tx, err := g.Store.pool.Begin(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin admission: %v", err)
	}
	defer tx.Rollback(ctx)
	var item Item
	var assigneeID *uuid.UUID
	var assigneeKind *string
	var active *uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id,org_id,repo_id,key,type,goal,acceptance,constraints,required_gates,assignee_id,assignee_kind,state,created_at,execution_claimed,active_execution_run_id
 FROM work.work_items WHERE org_id=$1 AND repo_id=$2 AND id=$3 FOR UPDATE`, scope.OrgID, repoID, itemID).Scan(&item.ID, &item.OrgID, &item.RepoID, &item.Key, &item.Type, &item.Goal, &item.Acceptance, &item.Constraints, &item.RequiredGates, &assigneeID, &assigneeKind, &item.State, &item.CreatedAt, &item.ExecutionClaimed, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "work item not found in repository")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "lock work: %v", err)
	}
	if assigneeID != nil {
		item.AssigneeID = *assigneeID
	}
	if assigneeKind != nil {
		item.AssigneeKind = *assigneeKind
	}
	var oldItem, oldRepo, oldAgent uuid.UUID
	var oldSponsor *uuid.UUID
	var intent []byte
	var outcome *string
	err = tx.QueryRow(ctx, `SELECT work_item_id,repo_id,agent_id,sponsor_id,intent,outcome FROM work.execution_claims WHERE org_id=$1 AND run_id=$2`, scope.OrgID, runID).Scan(&oldItem, &oldRepo, &oldAgent, &oldSponsor, &intent, &outcome)
	if err == nil {
		if oldItem != itemID || oldRepo != repoID || oldAgent != agentID || oldSponsor == nil || *oldSponsor != sponsorID || outcome != nil || active == nil || *active != runID {
			return nil, status.Error(codes.FailedPrecondition, "execution identity differs or has been released")
		}
		frozen := new(workv1.WorkItem)
		if err := proto.Unmarshal(intent, frozen); err != nil {
			return nil, status.Errorf(codes.Internal, "decode frozen intent: %v", err)
		}
		return &workv1.ClaimExecutionResponse{Item: frozen}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "read admission: %v", err)
	}
	if item.State != "open" || active != nil {
		return nil, status.Error(codes.FailedPrecondition, "work is not open for execution")
	}
	var maintenance, unapproved, blocked bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM work.maintenance_proposals WHERE org_id=$1 AND work_item_id=$2),
 EXISTS(SELECT 1 FROM work.maintenance_proposals WHERE org_id=$1 AND work_item_id=$2 AND (decision IS DISTINCT FROM 'approved' OR resolved_at IS NOT NULL)),
 EXISTS(SELECT 1 FROM work.work_item_deps d JOIN work.work_items b ON b.id=d.blocker_id WHERE d.blocked_id=$2 AND b.org_id=$1 AND b.state<>'done')`, scope.OrgID, itemID).Scan(&maintenance, &unapproved, &blocked)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "admission policy: %v", err)
	}
	if unapproved || blocked {
		return nil, status.Error(codes.FailedPrecondition, "work awaits maintenance approval or unfinished dependencies")
	}
	item.State = "in_progress"
	item.ExecutionClaimed = true
	frozen := toProtoItem(item)
	frozen.MaintenanceProposal = maintenance
	intent, err = proto.Marshal(frozen)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "freeze intent: %v", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO work.execution_claims(org_id,run_id,work_item_id,repo_id,agent_id,sponsor_id,intent) VALUES($1,$2,$3,$4,$5,$6,$7)`, scope.OrgID, runID, itemID, repoID, agentID, sponsorID, intent)
	if err != nil {
		return nil, status.Errorf(codes.Aborted, "record admission: %v", err)
	}
	_, err = tx.Exec(ctx, `UPDATE work.work_items SET state='in_progress',execution_claimed=true,active_execution_run_id=$3 WHERE org_id=$1 AND id=$2`, scope.OrgID, itemID, runID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "claim work: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, status.Errorf(codes.Internal, "commit admission: %v", err)
	}
	return &workv1.ClaimExecutionResponse{Item: frozen}, nil
}

// ReleaseExecution trusts only the runtime's durable terminal/fencing decision.
// A lost response is retried with the same outcome; a different outcome cannot
// rewrite history. Success is review, never proof of approval or completion.
func (g *GRPCServer) ReleaseExecution(ctx context.Context, req *workv1.ReleaseExecutionRequest) (*workv1.ReleaseExecutionResponse, error) {
	scope, err := executionOwner(ctx)
	if err != nil {
		return nil, err
	}
	itemID, err := executionID("work_item_id", req.GetWorkItemId())
	if err != nil {
		return nil, err
	}
	repoID, err := executionID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	runID, err := executionID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	agentID, err := executionID("agent_id", req.GetAgentId())
	if err != nil {
		return nil, err
	}
	var state string
	switch req.GetOutcome() {
	case "succeeded":
		state = "review"
	case "failed", "cancelled", "over_budget":
		state = "blocked"
	case "admission_failed":
		if !req.GetNoExecutionStarted() {
			return nil, status.Error(codes.FailedPrecondition, "admission release requires durable no-execution fencing")
		}
		state = "open"
	default:
		return nil, status.Error(codes.InvalidArgument, "unknown execution outcome")
	}
	if req.GetNoExecutionStarted() && req.GetOutcome() != "admission_failed" {
		return nil, status.Error(codes.InvalidArgument, "no_execution_started is only valid for admission_failed")
	}
	tx, err := g.Store.pool.Begin(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin release: %v", err)
	}
	defer tx.Rollback(ctx)
	var active *uuid.UUID
	err = tx.QueryRow(ctx, `SELECT active_execution_run_id FROM work.work_items WHERE org_id=$1 AND repo_id=$2 AND id=$3 FOR UPDATE`, scope.OrgID, repoID, itemID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "work item not found in repository")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "lock release: %v", err)
	}
	var outcome *string
	var oldItem, oldRepo, oldAgent uuid.UUID
	err = tx.QueryRow(ctx, `SELECT outcome,work_item_id,repo_id,agent_id FROM work.execution_claims WHERE org_id=$1 AND run_id=$2`, scope.OrgID, runID).Scan(&outcome, &oldItem, &oldRepo, &oldAgent)
	if errors.Is(err, pgx.ErrNoRows) {
		if req.GetOutcome() != "admission_failed" {
			return nil, status.Error(codes.NotFound, "execution claim not found")
		}
		// Cancellation can beat the claim RPC. Persist its identity under the same
		// item lock so a delayed request cannot resurrect execution after cleanup.
		_, err = tx.Exec(ctx, `INSERT INTO work.execution_claims(org_id,run_id,work_item_id,repo_id,agent_id,outcome,released_at) VALUES($1,$2,$3,$4,$5,'admission_failed',now())`, scope.OrgID, runID, itemID, repoID, agentID)
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "fence absent admission: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, status.Errorf(codes.Internal, "commit cancellation: %v", err)
		}
		return &workv1.ReleaseExecutionResponse{}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read release: %v", err)
	}
	if oldItem != itemID || oldRepo != repoID || oldAgent != agentID {
		return nil, status.Error(codes.FailedPrecondition, "execution binding differs")
	}
	if outcome != nil {
		if *outcome != req.GetOutcome() {
			return nil, status.Error(codes.FailedPrecondition, "execution already released with another outcome")
		}
		return &workv1.ReleaseExecutionResponse{}, nil
	}
	if active == nil || *active != runID {
		return nil, status.Error(codes.FailedPrecondition, "run does not own current execution")
	}
	_, err = tx.Exec(ctx, `UPDATE work.execution_claims SET outcome=$3,released_at=now() WHERE org_id=$1 AND run_id=$2`, scope.OrgID, runID, req.GetOutcome())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "record release: %v", err)
	}
	_, err = tx.Exec(ctx, `UPDATE work.work_items SET state=$3,active_execution_run_id=NULL WHERE org_id=$1 AND id=$2`, scope.OrgID, itemID, state)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "release work: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, status.Errorf(codes.Internal, "commit release: %v", err)
	}
	return &workv1.ReleaseExecutionResponse{}, nil
}
