package agents_test

import (
	"context"
	"fmt"
	"sync"

	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"google.golang.org/protobuf/proto"
)

// ownerFixture uses the REAL grant owner store. This adapter models already
// authenticated issuer delegation only for component tests; production must
// obtain it through the owner RPC, never by trusting persisted issuer fields.
type ownerFixture struct{ *capability.Store }

func (o ownerFixture) CancelIssuance(ctx context.Context, i capability.IssuanceIntent) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID != i.Grant.OrgID {
		return fmt.Errorf("foreign cleanup")
	}
	return o.Store.CancelIssuance(authz.WithScope(ctx, authz.Scope{OrgID: i.Grant.OrgID, ActorID: i.IssuerID, ActorKind: i.IssuerKind}), i)
}

// Work protocol double, not Work owner acceptance. The real row-lock ledger and
// authenticated claim/release race are a mandatory parent integration gate.
type workClaimsFixture struct {
	mu       sync.Mutex
	item     *workv1.WorkItem
	frozen   map[agents.ExecutionClaim]*workv1.WorkItem
	released map[agents.ExecutionClaim]bool
}

func (w *workClaimsFixture) ClaimExecution(ctx context.Context, c agents.ExecutionClaim) (*workv1.WorkItem, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.released[c] {
		return nil, fmt.Errorf("released claim")
	}
	if item := w.frozen[c]; item != nil {
		return proto.Clone(item).(*workv1.WorkItem), nil
	}
	if w.frozen == nil {
		w.frozen = make(map[agents.ExecutionClaim]*workv1.WorkItem)
	}
	if w.item == nil || w.item.GetId() != c.WorkItemID.String() || w.item.GetRepoId() != c.RepoID.String() {
		return nil, fmt.Errorf("claim identity mismatch")
	}
	w.frozen[c] = proto.Clone(w.item).(*workv1.WorkItem)
	return proto.Clone(w.item).(*workv1.WorkItem), nil
}
func (w *workClaimsFixture) ReleaseExecution(ctx context.Context, r agents.ExecutionRelease) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.released == nil {
		w.released = make(map[agents.ExecutionClaim]bool)
	}
	w.released[r.ExecutionClaim] = true
	return nil
}
