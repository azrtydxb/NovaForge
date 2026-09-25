package work_test

import (
	"context"
	"github.com/google/uuid"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/work"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"testing"
)

func TestHumanPatchPreservesAbsentFieldsAndRejectsStaleEdit(t *testing.T) {
	store := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	srv := work.NewGRPCServer(store)
	created, err := srv.CreateItem(ctx, &workv1.CreateItemRequest{RepoId: uuid.NewString(), Type: "feature", Goal: "original", Acceptance: []string{"keep"}, RequiredGates: []string{"tests"}})
	if err != nil {
		t.Fatal(err)
	}
	item := created.GetItem()
	t.Cleanup(func() { _, _ = store.PurgeRepository(ctx, uuid.MustParse(item.GetRepoId())) })
	req := &workv1.PatchItemRequest{Id: item.GetId(), RepoId: item.GetRepoId(), Expected: item, Values: &workv1.WorkItem{Goal: "revised"}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"goal"}}}
	changed, err := srv.PatchItem(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if changed.GetItem().GetGoal() != "revised" || len(changed.GetItem().GetAcceptance()) != 1 || changed.GetItem().GetRequiredGates()[0] != "tests" {
		t.Fatalf("patch lost fields: %v", changed)
	}
	if _, err = srv.PatchItem(ctx, req); err == nil {
		t.Fatal("stale edit overwrote a newer value")
	}
}

func TestHumanWorkEditsEnforceScopePolicyAndAtomicState(t *testing.T) {
	store := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	srv := work.NewGRPCServer(store)
	repo := uuid.New()
	t.Cleanup(func() { _, _ = store.PurgeRepository(ctx, repo) })
	create := func() *workv1.WorkItem {
		out, err := srv.CreateItem(ctx, &workv1.CreateItemRequest{RepoId: repo.String(), Type: "bug", Goal: "original"})
		if err != nil {
			t.Fatal(err)
		}
		return out.GetItem()
	}
	item := create()
	transition := &workv1.TransitionItemRequest{Id: item.GetId(), RepoId: repo.String(), ExpectedState: "open", ToState: "blocked"}
	changed, err := srv.TransitionItem(ctx, transition)
	if err != nil || changed.GetItem().GetState() != "blocked" {
		t.Fatalf("block: %v %v", changed, err)
	}
	if _, err := srv.TransitionItem(ctx, transition); err == nil {
		t.Fatal("stale state accepted")
	}
	if _, err := srv.TransitionItem(ctx, &workv1.TransitionItemRequest{Id: item.GetId(), RepoId: repo.String(), ExpectedState: "blocked", ToState: "done"}); err == nil {
		t.Fatal("human bypassed terminal evidence")
	}
	if _, err := srv.TransitionItem(ctx, &workv1.TransitionItemRequest{Id: item.GetId(), RepoId: repo.String(), ExpectedState: "blocked", ToState: "open"}); err != nil {
		t.Fatal(err)
	}
	patch := &workv1.PatchItemRequest{Id: item.GetId(), RepoId: repo.String(), Expected: item, Values: &workv1.WorkItem{Goal: "updated"}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"goal"}}}
	for name, bad := range map[string]context.Context{"anonymous": context.Background(), "other organization": scopedCtx(uuid.New()), "agent": authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorID: uuid.New(), ActorKind: "agent"}), "platform": authz.WithScope(context.Background(), authz.Scope{ActorKind: "service"})} {
		t.Run(name, func(t *testing.T) {
			if _, err := srv.PatchItem(bad, patch); err == nil {
				t.Fatal("unauthorized patch")
			}
			if _, err := srv.TransitionItem(bad, transition); err == nil {
				t.Fatal("unauthorized transition")
			}
		})
	}
	wrong := proto.Clone(patch).(*workv1.PatchItemRequest)
	wrong.RepoId = uuid.NewString()
	if _, err := srv.PatchItem(ctx, wrong); err == nil {
		t.Fatal("cross-repository patch")
	}
	wrong = proto.Clone(patch).(*workv1.PatchItemRequest)
	wrong.UpdateMask.Paths = []string{"state"}
	if _, err := srv.PatchItem(ctx, wrong); err == nil {
		t.Fatal("state patched")
	}
	wrong = proto.Clone(patch).(*workv1.PatchItemRequest)
	wrong.UpdateMask.Paths = []string{"required_gates"}
	wrong.Values.RequiredGates = []string{"invented"}
	if _, err := srv.PatchItem(ctx, wrong); err == nil {
		t.Fatal("unknown gate")
	}
	if err := store.Assign(ctx, uuid.MustParse(item.GetId()), uuid.New(), "agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.TransitionItem(ctx, transition); err == nil {
		t.Fatal("agent-owned transition")
	}
	proposal, err := store.ProposeFinding(ctx, org, repo, uuid.NewString(), work.Item{Type: "security", Goal: "scanner intent"})
	if err != nil {
		t.Fatal(err)
	}
	proposalOut, err := srv.GetItem(ctx, &workv1.GetItemRequest{Id: proposal.ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	if !proposalOut.GetItem().GetMaintenanceProposal() {
		t.Fatal("maintenance ownership absent from API")
	}
	patch.Id = proposal.ID.String()
	patch.Expected = proposalOut.GetItem()
	if _, err := srv.PatchItem(ctx, patch); err == nil {
		t.Fatal("scanner intent edited")
	}
	transition.Id = proposal.ID.String()
	if _, err := srv.TransitionItem(ctx, transition); err == nil {
		t.Fatal("proposal approval bypassed")
	}
	item = create()
	patch.Id = item.GetId()
	patch.Expected = item
	results := make(chan error, 2)
	for range 2 {
		go func() { _, err := srv.PatchItem(ctx, patch); results <- err }()
	}
	successes := 0
	for range 2 {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent stale editors: %d succeeded", successes)
	}
}
