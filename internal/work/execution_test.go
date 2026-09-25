package work_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/work"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func runtimeScope(org uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorKind: "service", ServiceName: "agent-runtime"})
}
func executionFixture(t *testing.T) (*work.Store, *work.GRPCServer, context.Context, context.Context, *workv1.WorkItem, *workv1.ClaimExecutionRequest) {
	t.Helper()
	store := newStore(t)
	srv := work.NewGRPCServer(store)
	org := uuid.New()
	human := scopedCtx(org)
	out, err := srv.CreateItem(human, &workv1.CreateItemRequest{RepoId: uuid.NewString(), Type: "feature", Goal: "frozen goal", Acceptance: []string{"must pass"}, Constraints: []string{"bounded"}, RequiredGates: []string{"tests"}})
	if err != nil {
		t.Fatal(err)
	}
	item := out.GetItem()
	scope, _ := authz.FromContext(human)
	return store, srv, human, runtimeScope(org), item, &workv1.ClaimExecutionRequest{WorkItemId: item.GetId(), RepoId: item.GetRepoId(), RunId: uuid.NewString(), AgentId: uuid.NewString(), SponsorId: scope.ActorID.String()}
}
func intentPatch(item *workv1.WorkItem) *workv1.PatchItemRequest {
	return &workv1.PatchItemRequest{Id: item.Id, RepoId: item.RepoId, Expected: item, Values: &workv1.WorkItem{Goal: "edited goal"}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"goal"}}}
}
func blockRequest(item *workv1.WorkItem) *workv1.TransitionItemRequest {
	return &workv1.TransitionItemRequest{Id: item.Id, RepoId: item.RepoId, ExpectedState: "open", ToState: "blocked"}
}
func TestExecutionClaimFreezesIntentAndFencesMutations(t *testing.T) {
	store, srv, human, runtime, item, req := executionFixture(t)
	claim, err := srv.ClaimExecution(runtime, req)
	if err != nil {
		t.Fatal(err)
	}
	if claim.GetItem().GetGoal() != item.Goal || len(claim.GetItem().GetAcceptance()) != 1 || !claim.GetItem().GetExecutionClaimed() {
		t.Fatalf("lost frozen intent: %v", claim)
	}
	if _, err := srv.PatchItem(human, intentPatch(item)); err == nil {
		t.Fatal("running intent edited")
	}
	if _, err := srv.TransitionItem(human, blockRequest(item)); err == nil {
		t.Fatal("running work blocked by unstarted transition")
	}
	// Generic controller state methods cannot undo an active admission.
	if err := store.SetState(human, uuid.MustParse(item.Id), "open"); err == nil {
		t.Fatal("generic setter reopened active claim")
	}
	if ok, err := store.TransitionState(human, uuid.MustParse(item.Id), "in_progress", "open"); err != nil || ok {
		t.Fatalf("generic transition bypass: %v %v", ok, err)
	}
	replay, err := srv.ClaimExecution(runtime, req)
	if err != nil || !proto.Equal(claim, replay) {
		t.Fatalf("replay differs: %v %v", replay, err)
	}
	for _, field := range []string{"run", "agent", "sponsor", "repo", "item"} {
		bad := proto.Clone(req).(*workv1.ClaimExecutionRequest)
		switch field {
		case "run":
			bad.RunId = uuid.NewString()
		case "agent":
			bad.AgentId = uuid.NewString()
		case "sponsor":
			bad.SponsorId = uuid.NewString()
		case "repo":
			bad.RepoId = uuid.NewString()
		case "item":
			bad.WorkItemId = uuid.NewString()
		}
		if _, err := srv.ClaimExecution(runtime, bad); err == nil {
			t.Fatalf("mismatched %s binding accepted", field)
		}
	}
	release := &workv1.ReleaseExecutionRequest{WorkItemId: item.Id, RepoId: item.RepoId, RunId: req.RunId, AgentId: req.AgentId, Outcome: "admission_failed"}
	if _, err := srv.ReleaseExecution(runtime, release); err == nil {
		t.Fatal("unfenced admission failure reopened work")
	}
	release.NoExecutionStarted = true
	if _, err := srv.ReleaseExecution(runtime, release); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ReleaseExecution(runtime, release); err != nil {
		t.Fatalf("release replay: %v", err)
	}
	if _, err := srv.ClaimExecution(runtime, req); err == nil {
		t.Fatal("released identity resurrected")
	}
	if _, err := srv.PatchItem(human, intentPatch(item)); err == nil {
		t.Fatal("historically claimed intent edited")
	}
	if _, err := srv.TransitionItem(human, blockRequest(item)); err == nil {
		t.Fatal("historical claim bypassed block guard")
	}
	// A fresh controller-authorized attempt is not permanently prohibited.
	req.RunId = uuid.NewString()
	if _, err := srv.ClaimExecution(runtime, req); err != nil {
		t.Fatalf("new admission after fenced failure: %v", err)
	}
	release.RunId = req.RunId
	release.Outcome = "succeeded"
	release.NoExecutionStarted = false
	if _, err := srv.ReleaseExecution(runtime, release); err != nil {
		t.Fatal(err)
	}
	got, err := srv.GetItem(human, &workv1.GetItemRequest{Id: item.Id})
	if err != nil || got.GetItem().GetState() != "review" {
		t.Fatalf("success must stop at review: %v %v", got, err)
	}
	release.Outcome = "failed"
	if _, err := srv.ReleaseExecution(runtime, release); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("conflicting release: %v", err)
	}
}

func TestExecutionAdmissionRejectsBlockedAndWrongService(t *testing.T) {
	_, srv, human, runtime, item, req := executionFixture(t)
	sc, _ := authz.FromContext(runtime)
	for _, bad := range []context.Context{context.Background(), human, runtimeScope(uuid.New()), authz.WithScope(context.Background(), authz.Scope{OrgID: sc.OrgID, ActorKind: "agent", ServiceName: "agent-runtime"}), authz.WithScope(context.Background(), authz.Scope{OrgID: sc.OrgID, ActorKind: "service", ServiceName: "ci-runner"}), authz.WithScope(context.Background(), authz.Scope{ActorKind: "service", ServiceName: "agent-runtime"})} {
		if _, err := srv.ClaimExecution(bad, req); err == nil {
			t.Fatal("unauthorized claim")
		}
		if _, err := srv.ReleaseExecution(bad, &workv1.ReleaseExecutionRequest{WorkItemId: item.Id, RepoId: item.RepoId, RunId: req.RunId, AgentId: req.AgentId, Outcome: "failed"}); err == nil {
			t.Fatal("unauthorized release")
		}
	}
	if _, err := srv.TransitionItem(human, blockRequest(item)); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ClaimExecution(runtime, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("blocked admission: %v", err)
	}
}

func TestExecutionAdmissionSerializesWithEditAndBlock(t *testing.T) {
	for _, mode := range []string{"edit", "block", "claim"} {
		t.Run(mode, func(t *testing.T) {
			for range 12 {
				_, srv, human, runtime, item, req := executionFixture(t)
				var claim *workv1.ClaimExecutionResponse
				var claimErr, otherErr error
				start := make(chan struct{})
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); <-start; claim, claimErr = srv.ClaimExecution(runtime, req) }()
				go func() {
					defer wg.Done()
					<-start
					switch mode {
					case "edit":
						_, otherErr = srv.PatchItem(human, intentPatch(item))
					case "block":
						_, otherErr = srv.TransitionItem(human, blockRequest(item))
					case "claim":
						other := proto.Clone(req).(*workv1.ClaimExecutionRequest)
						other.RunId = uuid.NewString()
						_, otherErr = srv.ClaimExecution(runtime, other)
					}
				}()
				close(start)
				wg.Wait()
				if mode == "edit" {
					if claimErr != nil {
						t.Fatalf("edit cannot prevent later claim: %v", claimErr)
					}
					want := "frozen goal"
					if otherErr == nil {
						want = "edited goal"
					}
					if claim.GetItem().GetGoal() != want {
						t.Fatalf("claim did not freeze serialized intent: %v edit=%v", claim, otherErr)
					}
				} else if (claimErr == nil) == (otherErr == nil) {
					t.Fatalf("must have exactly one winner: claim=%v %s=%v", claimErr, mode, otherErr)
				}
			}
		})
	}
}

func TestExecutionCancellationTombstoneFencesLateClaim(t *testing.T) {
	store, srv, human, runtime, item, req := executionFixture(t)
	release := &workv1.ReleaseExecutionRequest{WorkItemId: item.Id, RepoId: item.RepoId, RunId: req.RunId, AgentId: req.AgentId, Outcome: "admission_failed", NoExecutionStarted: true}
	if _, err := srv.ReleaseExecution(runtime, release); err != nil {
		t.Fatalf("absent claim cancellation: %v", err)
	}
	// A new server over the same database retains the fence, not a process map.
	srv = work.NewGRPCServer(store)
	if _, err := srv.ClaimExecution(runtime, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("late claim resurrected: %v", err)
	}
	if _, err := srv.ReleaseExecution(runtime, release); err != nil {
		t.Fatal(err)
	}
	release.AgentId = uuid.NewString()
	if _, err := srv.ReleaseExecution(runtime, release); err == nil {
		t.Fatal("mismatched tombstone replay accepted")
	}
	got, err := srv.GetItem(human, &workv1.GetItemRequest{Id: item.Id})
	if err != nil || got.GetItem().GetState() != "open" || got.GetItem().GetExecutionClaimed() {
		t.Fatalf("absent cancellation changed work: %v %v", got, err)
	}
}

func TestExecutionClaimRacesCancellation(t *testing.T) {
	for range 12 {
		_, srv, human, runtime, item, req := executionFixture(t)
		release := &workv1.ReleaseExecutionRequest{WorkItemId: item.Id, RepoId: item.RepoId, RunId: req.RunId, AgentId: req.AgentId, Outcome: "admission_failed", NoExecutionStarted: true}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var releaseErr error
		go func() { defer wg.Done(); <-start; _, _ = srv.ClaimExecution(runtime, req) }()
		go func() { defer wg.Done(); <-start; _, releaseErr = srv.ReleaseExecution(runtime, release) }()
		close(start)
		wg.Wait()
		if releaseErr != nil {
			t.Fatal(releaseErr)
		}
		if _, err := srv.ClaimExecution(runtime, req); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("claim after acknowledged cancellation: %v", err)
		}
		got, err := srv.GetItem(human, &workv1.GetItemRequest{Id: item.Id})
		if err != nil || got.GetItem().GetState() != "open" {
			t.Fatalf("cancel state: %v %v", got, err)
		}
	}
}

func TestExecutionAdmissionPolicyAndOutcomes(t *testing.T) {
	store, srv, human, runtime, item, req := executionFixture(t)
	sc, _ := authz.FromContext(human)
	blocker, err := store.Create(human, work.Item{OrgID: sc.OrgID, RepoID: uuid.MustParse(item.RepoId), Type: "bug", Goal: "blocker"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddDependency(human, uuid.MustParse(item.Id), blocker.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ClaimExecution(runtime, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("dependency bypass: %v", err)
	}
	if err := store.SetState(human, blocker.ID, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ClaimExecution(runtime, req); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"", "done", "merged"} {
		if _, err := srv.ReleaseExecution(runtime, &workv1.ReleaseExecutionRequest{WorkItemId: item.Id, RepoId: item.RepoId, RunId: req.RunId, AgentId: req.AgentId, Outcome: outcome}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid outcome %q: %v", outcome, err)
		}
	}
	if _, err := srv.ReleaseExecution(runtime, &workv1.ReleaseExecutionRequest{WorkItemId: item.Id, RepoId: item.RepoId, RunId: uuid.NewString(), AgentId: req.AgentId, Outcome: "succeeded"}); status.Code(err) != codes.NotFound {
		t.Fatalf("fabricated success: %v", err)
	}
	if _, err := srv.ReleaseExecution(runtime, &workv1.ReleaseExecutionRequest{WorkItemId: item.Id, RepoId: item.RepoId, RunId: req.RunId, AgentId: req.AgentId, Outcome: "over_budget"}); err != nil {
		t.Fatal(err)
	}
	got, err := srv.GetItem(human, &workv1.GetItemRequest{Id: item.Id})
	if err != nil || got.GetItem().GetState() != "blocked" {
		t.Fatalf("budget outcome: %v %v", got, err)
	}
	proposal, err := store.ProposeFinding(human, sc.OrgID, uuid.MustParse(item.RepoId), "claim-policy", work.Item{Type: "security", Goal: "maintain"})
	if err != nil {
		t.Fatal(err)
	}
	req.WorkItemId = proposal.ID.String()
	req.RunId = uuid.NewString()
	if _, err := srv.ClaimExecution(runtime, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unapproved execution: %v", err)
	}
	if _, err := store.ApproveProposal(human, uuid.MustParse(item.RepoId), "claim-policy", uuid.Nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ClaimExecution(runtime, req); err != nil {
		t.Fatal(err)
	}
	if err := store.ResolveProposal(human, sc.OrgID, uuid.MustParse(item.RepoId), "claim-policy", "resolved while running"); err == nil {
		t.Fatal("maintenance scanner completed active execution")
	}
}
