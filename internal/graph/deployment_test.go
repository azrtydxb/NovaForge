package graph_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/svcauth"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestObservedArtifactHasOwnNodeKind(t *testing.T) {
	s := newStore(t)
	org := uuid.New()
	_, err := s.UpsertNode(scopedCtx(org, uuid.New()), graph.Node{OrgID: org, Kind: "artifact", Key: uuid.NewString(), Attrs: map[string]string{"provenance": "observed"}})
	if err != nil {
		t.Fatalf("artifact cannot be represented without coercing a service/commit: %v", err)
	}
}

func observedEvent() graph.DeploymentSuccessEvent {
	op := uuid.New()
	return graph.DeploymentSuccessEvent{EvidenceID: op.String() + ":1", OrgID: uuid.New(), RepoID: uuid.New(), OperationID: op, RunID: uuid.New(), Artifact: "sha256:" + strings.Repeat("a", 64), Target: "staging", Destination: "cluster/ns/release", TargetRevision: "config-v1", LatestExecuteAttempt: 1, ExternalID: "helm-release:revision:3"}
}
func observedScope(t *testing.T, org uuid.UUID) context.Context {
	t.Helper()
	tok, err := svcauth.Mint("deployment-graph-test", graph.DeploymentEventWorker, org, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := svcauth.ScopeFromToken("deployment-graph-test", tok)
	if err != nil {
		t.Fatal(err)
	}
	return authz.WithScope(t.Context(), scope)
}
func TestDeploymentSuccessWireContract(t *testing.T) {
	// Exact JSON from delivery.SuccessEvent (outbox.go pinned ad26312b6c1c4a00
	// fed19e89dd808e8f89d81219414a8072d76c199ee23b46ed), not a graph-only shape.
	raw := `{"evidence_id":"33333333-3333-4333-8333-333333333333:2","org_id":"11111111-1111-4111-8111-111111111111","repo_id":"22222222-2222-4222-8222-222222222222","operation_id":"33333333-3333-4333-8333-333333333333","run_id":"44444444-4444-4444-8444-444444444444","artifact":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","target":"staging","destination":"cluster/ns/release","target_revision":"config-v1","latest_execute_attempt":2,"external_id":"helm:3"}`
	var e graph.DeploymentSuccessEvent
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatal(err)
	}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(e)
	if err != nil || string(got) != raw {
		t.Fatalf("wire drift: %s %v", got, err)
	}
	if graph.StreamDeploymentSucceeded != "stream:deployment:succeeded" {
		t.Fatal("stream drift")
	}
}
func TestDeploymentProjectionScopedImmutableReplayAndPurge(t *testing.T) {
	s := newStore(t)
	e := observedEvent()
	ctx := observedScope(t, e.OrgID)
	for _, bad := range []context.Context{t.Context(), scopedCtx(e.OrgID, uuid.New()), observedScope(t, uuid.New()), authz.WithScope(t.Context(), authz.Scope{ActorKind: "service", PlatformWorker: graph.DeploymentEventWorker}), authz.WithScope(t.Context(), authz.Scope{OrgID: e.OrgID, ActorKind: "service", ServiceName: "other-worker"})} {
		if err := s.RecordDeploymentSuccess(bad, e); err == nil {
			t.Fatal("unauthorized projection accepted")
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.RecordDeploymentSuccess(ctx, e) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var artifactID uuid.UUID
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2`, e.OrgID, e.RepoID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("duplicate nodes: %d %v", count, err)
	}
	if err := s.Pool().QueryRow(ctx, `SELECT id FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND kind='artifact'`, e.OrgID, e.RepoID).Scan(&artifactID); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.Neighbours(ctx, artifactID, "deployed_as", "out")
	if err != nil || len(nodes) != 1 || nodes[0].Kind != "deployment" || nodes[0].Attrs["external_id"] != e.ExternalID || nodes[0].Attrs["provenance"] != "observed" {
		t.Fatalf("observed edge missing: %+v %v", nodes, err)
	}
	if nodes, err = s.Neighbours(observedScope(t, uuid.New()), artifactID, "deployed_as", "out"); err != nil || len(nodes) != 0 {
		t.Fatalf("foreign graph leak: %+v %v", nodes, err)
	}
	for _, change := range []func(*graph.DeploymentSuccessEvent){func(v *graph.DeploymentSuccessEvent) { v.Artifact = "sha256:" + strings.Repeat("b", 64) }, func(v *graph.DeploymentSuccessEvent) { v.RepoID = uuid.New() }, func(v *graph.DeploymentSuccessEvent) { v.ExternalID = "different" }} {
		bad := e
		change(&bad)
		if err := s.RecordDeploymentSuccess(ctx, bad); err == nil {
			t.Fatal("conflicting replay accepted")
		}
	}
	// Source/declared refresh must not erase observed operational history.
	if err := s.SetIndexCheckpoint(ctx, e.OrgID, e.RepoID, "", "refresh-test"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceDeclaredRelationships(ctx, e.OrgID, e.RepoID, strings.Repeat("b", 40), nil); err != nil {
		t.Fatal(err)
	}
	nodes, err = s.Neighbours(ctx, artifactID, "deployed_as", "out")
	if err != nil || len(nodes) != 1 {
		t.Fatalf("refresh erased deployment history: %+v %v", nodes, err)
	}
	// Later observed executions are immutable history, not replacement current state.
	later := e
	later.LatestExecuteAttempt = 2
	later.EvidenceID = e.OperationID.String() + ":" + strconv.Itoa(later.LatestExecuteAttempt)
	later.ExternalID = "helm:4"
	if err := s.RecordDeploymentSuccess(ctx, later); err != nil {
		t.Fatal(err)
	}
	foreign := e
	foreign.OrgID = uuid.New()
	if err := s.RecordDeploymentSuccess(observedScope(t, foreign.OrgID), foreign); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeRepository(ctx, e.RepoID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDeploymentSuccess(ctx, e); !errors.Is(err, graph.ErrIndexDeleted) {
		t.Fatalf("late event resurrected: %v", err)
	}
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM graph.graph_nodes WHERE org_id=$1`, e.OrgID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("purge leak: %d %v", count, err)
	}
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM graph.graph_nodes WHERE org_id=$1`, foreign.OrgID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("foreign purge: %d %v", count, err)
	}
	other := observedEvent()
	other.OrgID = e.OrgID
	if err := s.RecordDeploymentSuccess(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeOrganization(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDeploymentSuccess(ctx, other); !errors.Is(err, graph.ErrIndexDeleted) {
		t.Fatalf("org resurrection: %v", err)
	}
}
func TestDeploymentProjectionPurgeWaitsForFence(t *testing.T) {
	s := newStore(t)
	e := observedEvent()
	ctx := observedScope(t, e.OrgID)
	locked, release, err := s.LockIndex(ctx, e.OrgID, e.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := make(chan error, 1)
	go func() { done <- s.PurgeRepository(ctx, e.RepoID) }()
	select {
	case err := <-done:
		t.Fatalf("purge passed held fence: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := s.RecordDeploymentSuccess(locked, e); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("purge did not finish")
	}
	if err := s.RecordDeploymentSuccess(ctx, e); !errors.Is(err, graph.ErrIndexDeleted) {
		t.Fatalf("late replay: %v", err)
	}
	if err := s.RecordDeploymentSuccess(locked, e); err == nil {
		t.Fatal("released session accepted")
	}
}

func TestDeploymentEvidenceCannotRetargetAfterPurge(t *testing.T) {
	s := newStore(t)
	e := observedEvent()
	ctx := observedScope(t, e.OrgID)
	if err := s.RecordDeploymentSuccess(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeRepository(ctx, e.RepoID); err != nil {
		t.Fatal(err)
	}
	moved := e
	moved.RepoID = uuid.New()
	if err := s.RecordDeploymentSuccess(ctx, moved); err == nil {
		t.Fatal("purged evidence retargeted to another repository")
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM graph.graph_nodes WHERE org_id=$1`, e.OrgID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("retarget materialized graph: %d %v", n, err)
	}
	// Multiple consumers racing different new repositories must all reject the
	// original evidence identity, even though each holds a different repo fence.
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copy := e
			copy.RepoID = uuid.New()
			errs <- s.RecordDeploymentSuccess(ctx, copy)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err == nil {
			t.Fatal("racing replay retargeted purged evidence")
		}
	}
}

func TestDeploymentIdentitySerializesConflictingFirstObservations(t *testing.T) {
	s := newStore(t)
	first := observedEvent()
	second := first
	second.RepoID = uuid.New()
	ctx := observedScope(t, first.OrgID)
	type result struct {
		repo uuid.UUID
		err  error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, event := range []graph.DeploymentSuccessEvent{first, second} {
		go func(e graph.DeploymentSuccessEvent) {
			<-start
			results <- result{e.RepoID, s.RecordDeploymentSuccess(ctx, e)}
		}(event)
	}
	close(start)
	a, b := <-results, <-results
	if (a.err == nil) == (b.err == nil) {
		t.Fatalf("expected exactly one identity winner: %v %v", a.err, b.err)
	}
	winner := a
	if winner.err != nil {
		winner = b
	}
	var bound uuid.UUID
	if err := s.Pool().QueryRow(ctx, `SELECT repo_id FROM graph.deployment_evidence_identities WHERE org_id=$1 AND evidence_id=$2`, first.OrgID, first.EvidenceID).Scan(&bound); err != nil || bound != winner.repo {
		t.Fatalf("identity not serialized: %s %v", bound, err)
	}
	if err := s.PurgeOrganization(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool().QueryRow(ctx, `SELECT repo_id FROM graph.deployment_evidence_identities WHERE org_id=$1 AND evidence_id=$2`, first.OrgID, first.EvidenceID).Scan(&bound); err != nil || bound != winner.repo {
		t.Fatalf("minimal identity lost at org purge: %s %v", bound, err)
	}
}

func TestDeploymentFirstReceivedAfterPurgeStillBindsIdentity(t *testing.T) {
	s := newStore(t)
	e := observedEvent()
	ctx := observedScope(t, e.OrgID)
	if err := s.PurgeRepository(ctx, e.RepoID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDeploymentSuccess(ctx, e); !errors.Is(err, graph.ErrIndexDeleted) {
		t.Fatalf("deleted repo accepted: %v", err)
	}
	moved := e
	moved.RepoID = uuid.New()
	if err := s.RecordDeploymentSuccess(ctx, moved); err == nil {
		t.Fatal("first post-purge observation could retarget")
	}
}
