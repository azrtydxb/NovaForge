package work_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/work"
)

func actorCtx(orgID, actor uuid.UUID, kind string) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{OrgID: orgID, ActorID: actor, ActorKind: kind})
}

// propose raises a finding exactly as the maintenance proposer does: under a
// system scope, with no person behind it.
func propose(t *testing.T, store *work.Store, orgID, repoID uuid.UUID, goal string) (string, work.Item) {
	t.Helper()
	fp := "fp-" + uuid.NewString()
	item, err := store.ProposeFinding(actorCtx(orgID, uuid.Nil, "system"), orgID, repoID, fp,
		work.Item{Type: "security", Goal: goal})
	if err != nil {
		t.Fatalf("ProposeFinding: %v", err)
	}
	return fp, item
}

func findProposal(t *testing.T, srv *work.GRPCServer, ctx context.Context, repoID uuid.UUID, fp string) *workv1.MaintenanceProposal {
	t.Helper()
	resp, err := srv.ListMaintenanceProposals(ctx, &workv1.ListMaintenanceProposalsRequest{RepoId: repoID.String()})
	if err != nil {
		t.Fatalf("ListMaintenanceProposals: %v", err)
	}
	for _, p := range resp.GetProposals() {
		if p.GetFingerprint() == fp {
			return p
		}
	}
	t.Fatalf("proposal %s not listed", fp)
	return nil
}

// TestProposalAwaitsApprovalUntilAPersonApproves pins the approval the design
// requires before a maintenance finding is acted on (section 23: "wait for
// policy or human approval before execution"). A proposal was a plain open
// Work Item that nothing distinguished from any other, so nothing could
// refuse to execute it, and there was no way to approve one at all.
func TestProposalAwaitsApprovalUntilAPersonApproves(t *testing.T) {
	store := newStore(t)
	srv := work.NewGRPCServer(store)
	orgID, repoID, person := uuid.New(), uuid.New(), uuid.New()
	fp, item := propose(t, store, orgID, repoID, "CVE in a dependency")
	personCtx := actorCtx(orgID, person, "user")

	got, err := srv.GetItem(personCtx, &workv1.GetItemRequest{Key: item.Key})
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if !got.GetItem().GetAwaitingApproval() {
		t.Fatal("a fresh proposal is not marked as awaiting approval")
	}

	// Approval is a person's decision: an agent must not approve work in
	// order to do it, and a platform worker is not anyone.
	for _, kind := range []string{"agent", "system", "service"} {
		_, err := srv.ApproveMaintenanceProposal(actorCtx(orgID, uuid.New(), kind),
			&workv1.ApproveMaintenanceProposalRequest{RepoId: repoID.String(), Fingerprint: fp})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("approval by %s: code = %v, want PermissionDenied", kind, status.Code(err))
		}
	}

	// Another organization cannot approve it, even by naming it exactly.
	_, err = srv.ApproveMaintenanceProposal(actorCtx(uuid.New(), person, "user"),
		&workv1.ApproveMaintenanceProposalRequest{RepoId: repoID.String(), Fingerprint: fp})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("cross-org approval: code = %v, want NotFound", status.Code(err))
	}

	resp, err := srv.ApproveMaintenanceProposal(personCtx,
		&workv1.ApproveMaintenanceProposalRequest{RepoId: repoID.String(), Fingerprint: fp})
	if err != nil {
		t.Fatalf("ApproveMaintenanceProposal: %v", err)
	}
	p := resp.GetProposal()
	if p.GetDecision() != "approved" || p.GetDecidedBy() != person.String() || p.GetDecidedAt() == "" {
		t.Fatalf("approval not recorded: %+v", p)
	}
	// With no assignee named, the person who approved it holds it.
	if p.GetAssigneeId() != person.String() || p.GetAssigneeKind() != "user" {
		t.Fatalf("approved proposal assigned to %s (%s), want the approver", p.GetAssigneeId(), p.GetAssigneeKind())
	}
	listed := findProposal(t, srv, personCtx, repoID, fp)
	if listed.GetDecision() != "approved" || listed.GetDecidedBy() != person.String() {
		t.Fatalf("listed proposal does not show the approval: %+v", listed)
	}

	got, err = srv.GetItem(personCtx, &workv1.GetItemRequest{Key: item.Key})
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got.GetItem().GetAwaitingApproval() {
		t.Fatal("an approved proposal is still awaiting approval")
	}
	if got.GetItem().GetState() != "open" {
		t.Fatalf("approved item state = %q, want open (actionable)", got.GetItem().GetState())
	}

	// A decision is made once.
	_, err = srv.ApproveMaintenanceProposal(personCtx,
		&workv1.ApproveMaintenanceProposalRequest{RepoId: repoID.String(), Fingerprint: fp})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("second approval: code = %v, want FailedPrecondition", status.Code(err))
	}
}

// TestApproveAssignsTheChosenAgent pins that approval can hand the work to an
// agent directly — the point of approving a fix is usually to have it made.
func TestApproveAssignsTheChosenAgent(t *testing.T) {
	store := newStore(t)
	srv := work.NewGRPCServer(store)
	orgID, repoID, person, agent := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	fp, item := propose(t, store, orgID, repoID, "outdated dependency")
	ctx := actorCtx(orgID, person, "user")

	_, err := srv.ApproveMaintenanceProposal(ctx, &workv1.ApproveMaintenanceProposalRequest{
		RepoId: repoID.String(), Fingerprint: fp, AssigneeId: agent.String(), AssigneeKind: "robot",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown assignee kind: code = %v, want InvalidArgument", status.Code(err))
	}

	resp, err := srv.ApproveMaintenanceProposal(ctx, &workv1.ApproveMaintenanceProposalRequest{
		RepoId: repoID.String(), Fingerprint: fp, AssigneeId: agent.String(), AssigneeKind: "agent",
	})
	if err != nil {
		t.Fatalf("ApproveMaintenanceProposal: %v", err)
	}
	if resp.GetProposal().GetDecidedBy() != person.String() {
		t.Fatalf("decided_by = %s, want the approving person", resp.GetProposal().GetDecidedBy())
	}
	got, err := srv.GetItem(ctx, &workv1.GetItemRequest{Key: item.Key})
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got.GetItem().GetAssigneeId() != agent.String() || got.GetItem().GetAssigneeKind() != "agent" {
		t.Fatalf("item assigned to %s (%s), want the chosen agent", got.GetItem().GetAssigneeId(), got.GetItem().GetAssigneeKind())
	}
}

// TestDismissClosesAProposalWithItsReason pins the other decision: a proposal
// a person declines is closed, with the reason recorded where the Work Item's
// readers will find it, and it is not reopened or rewritten by a later scan.
func TestDismissClosesAProposalWithItsReason(t *testing.T) {
	store := newStore(t)
	srv := work.NewGRPCServer(store)
	orgID, repoID, person := uuid.New(), uuid.New(), uuid.New()
	fp, item := propose(t, store, orgID, repoID, "dead code in legacy module")
	ctx := actorCtx(orgID, person, "user")

	_, err := srv.DismissMaintenanceProposal(ctx, &workv1.DismissMaintenanceProposalRequest{
		RepoId: repoID.String(), Fingerprint: fp, Reason: "   ",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("dismissal without a reason: code = %v, want InvalidArgument", status.Code(err))
	}
	_, err = srv.DismissMaintenanceProposal(actorCtx(orgID, uuid.New(), "agent"), &workv1.DismissMaintenanceProposalRequest{
		RepoId: repoID.String(), Fingerprint: fp, Reason: "agents do not decide this",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("dismissal by an agent: code = %v, want PermissionDenied", status.Code(err))
	}

	const reason = "the module is scheduled for removal next quarter"
	resp, err := srv.DismissMaintenanceProposal(ctx, &workv1.DismissMaintenanceProposalRequest{
		RepoId: repoID.String(), Fingerprint: fp, Reason: reason,
	})
	if err != nil {
		t.Fatalf("DismissMaintenanceProposal: %v", err)
	}
	p := resp.GetProposal()
	if p.GetDecision() != "dismissed" || p.GetDismissReason() != reason || p.GetDecidedBy() != person.String() {
		t.Fatalf("dismissal not recorded: %+v", p)
	}

	got, err := srv.GetItem(ctx, &workv1.GetItemRequest{Key: item.Key})
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got.GetItem().GetState() != "done" || got.GetItem().GetAwaitingApproval() {
		t.Fatalf("dismissed item: state %q awaiting %v, want done and not awaiting", got.GetItem().GetState(), got.GetItem().GetAwaitingApproval())
	}
	comments, err := store.ListComments(ctx, item.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	var recorded bool
	for _, c := range comments {
		if strings.Contains(c.Body, reason) && c.AuthorID == person {
			recorded = true
		}
	}
	if !recorded {
		t.Fatal("the dismissal reason is not on the Work Item's discussion")
	}

	if _, err := srv.ApproveMaintenanceProposal(ctx, &workv1.ApproveMaintenanceProposalRequest{
		RepoId: repoID.String(), Fingerprint: fp,
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("approving a dismissed proposal: code = %v, want FailedPrecondition", status.Code(err))
	}

	// The finding stops reproducing on a later scan: the proposer must not
	// rewrite a Work Item a person already closed with a reason.
	sys := actorCtx(orgID, uuid.Nil, "system")
	open, err := store.OpenProposalFingerprints(sys, orgID, repoID)
	if err != nil {
		t.Fatalf("OpenProposalFingerprints: %v", err)
	}
	if _, ok := open[fp]; ok {
		t.Fatal("a dismissed proposal is still reported open to the proposer")
	}
	if err := store.ResolveProposal(sys, orgID, repoID, fp, "no longer detected"); err != nil {
		t.Fatalf("ResolveProposal: %v", err)
	}
	after, err := srv.GetItem(ctx, &workv1.GetItemRequest{Key: item.Key})
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if after.GetItem().GetGoal() != got.GetItem().GetGoal() {
		t.Fatalf("a later scan rewrote a dismissed Work Item's goal to %q", after.GetItem().GetGoal())
	}
}
