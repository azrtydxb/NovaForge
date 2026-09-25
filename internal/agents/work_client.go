package agents

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc/metadata"
	"time"
)

type WorkExecutionClient struct {
	Work       workv1.WorkServiceClient
	HMACSecret string
}

func (c WorkExecutionClient) context(ctx context.Context) (context.Context, context.CancelFunc, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return nil, nil, fmt.Errorf("Work execution organization required")
	}
	token, err := svcauth.Mint(c.HMACSecret, "agent-runtime", scope.OrgID, svcauth.DefaultTTL)
	if err != nil {
		return nil, nil, err
	}
	// Drop incoming metadata too: a forwarding client must not append the original
	// user's authorization after the exact service credential on owner calls.
	ctx = metadata.NewIncomingContext(ctx, metadata.MD{})
	ctx, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), 5*time.Second)
	return ctx, cancel, nil
}
func (c WorkExecutionClient) ClaimExecution(ctx context.Context, i ExecutionClaim) (*workv1.WorkItem, error) {
	ctx, cancel, err := c.context(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	r, err := c.Work.ClaimExecution(ctx, &workv1.ClaimExecutionRequest{WorkItemId: i.WorkItemID.String(), RepoId: i.RepoID.String(), RunId: i.RunID.String(), AgentId: i.AgentID.String(), SponsorId: i.SponsorID.String()})
	if err != nil {
		return nil, err
	}
	return r.GetItem(), nil
}
func (c WorkExecutionClient) ReleaseExecution(ctx context.Context, i ExecutionRelease) error {
	ctx, cancel, err := c.context(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	_, err = c.Work.ReleaseExecution(ctx, &workv1.ReleaseExecutionRequest{WorkItemId: i.WorkItemID.String(), RepoId: i.RepoID.String(), RunId: i.RunID.String(), AgentId: i.AgentID.String(), Outcome: i.Outcome, NoExecutionStarted: i.NoExecutionStarted})
	return err
}
