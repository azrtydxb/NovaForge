package agents

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
)

// CheckBranchLock answers whether a ref is held by a running Agent Run in the
// caller's organization, and by which run and agent.
//
// It exists because the lock is the run's own state, in this service's
// schema, and the service that must enforce it on a push is git-platform,
// which may not read that schema. Answering from the run row — rather than
// keeping a copy of the lock elsewhere — means no terminal path (a finished
// loop, a cancel, a run over budget, a crashed replica's orphan recovered
// later) has a second record to release.
func (g *GRPCServer) CheckBranchLock(ctx context.Context, req *agentsv1.CheckBranchLockRequest) (*agentsv1.CheckBranchLockResponse, error) {
	if _, err := callerOrg(ctx); err != nil {
		return nil, err
	}
	repoID, err := parseUUID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	if req.GetRef() == "" {
		return nil, status.Error(codes.InvalidArgument, "ref is required")
	}
	h, locked, err := NewBranchLock(g.Store.Pool()).Check(ctx, repoID, req.GetRef())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check branch lock: %v", err)
	}
	if !locked {
		return &agentsv1.CheckBranchLockResponse{}, nil
	}
	return &agentsv1.CheckBranchLockResponse{
		Locked:  true,
		RunId:   h.RunID.String(),
		AgentId: h.AgentID.String(),
		Prefix:  h.Prefix,
	}, nil
}
