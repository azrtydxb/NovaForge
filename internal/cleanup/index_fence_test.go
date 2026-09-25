package cleanup_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/cleanup"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/knowledge"
)

// This is the actual Redis deletion consumer and graph store, not a direct
// purge call. XACK must follow the fence and purge, never the announcement.
func TestEngineeringGraphDeletionConsumerWaitsForIndexFence(t *testing.T) {
	migrate(t, "graph", graph.MigrationsFS)
	migrate(t, "knowledge", knowledge.MigrationsFS)
	p, r := pool(t), rdb(t)
	store := graph.NewStore(p)
	org, repo := uuid.New(), uuid.New()
	scoped := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorKind: "service"})
	indexed, release, err := store.LockIndex(scoped, org, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := store.ReplaceFileIndex(indexed, graph.FileIndex{OrgID: org, RepoID: repo, Path: "a.go"}); err != nil {
		t.Fatal(err)
	}
	h := cleanup.EngineeringGraph(store, knowledge.NewStore(p))
	h.RepoStream = "stream:test:index-fence:" + uuid.NewString()
	h.OrgDeleted = nil
	t.Cleanup(func() { r.Del(context.Background(), h.RepoStream) })
	started := make(chan struct{})
	done := make(chan error, 1)
	original := h.RepoDeleted
	h.RepoDeleted = func(ctx context.Context, e events.RepoDeletedEvent) error {
		close(started)
		err := original(ctx, e)
		done <- err
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h.Run(ctx, r, "fixture")
	if err := events.Publish(ctx, r, h.RepoStream, events.RepoDeletedEvent{OrgID: org, RepoID: repo}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("deletion consumer never received event")
	}
	select {
	case err := <-done:
		t.Fatalf("deletion passed live index fence: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	// Already committed healthy files and further writes on the live session
	// survive until the writer relinquishes its fence, then all are purged.
	if err := store.ReplaceFileIndex(indexed, graph.FileIndex{OrgID: org, RepoID: repo, Path: "b.go"}); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("purge did not resume after release")
	}
	for {
		pending, err := r.XPending(ctx, h.RepoStream, h.Service).Result()
		if err != nil {
			t.Fatal(err)
		}
		if pending.Count == 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("purge was not acknowledged")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := count(t, p, `SELECT count(*) FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2`, org, repo); n != 0 {
		t.Fatalf("purge left %d nodes", n)
	}
	if _, release, err := store.LockIndex(scoped, org, repo); !errors.Is(err, graph.ErrIndexDeleted) {
		if release != nil {
			release()
		}
		t.Fatalf("late writer admitted: %v", err)
	}
	if err := store.ReplaceFileIndex(scoped, graph.FileIndex{OrgID: org, RepoID: uuid.New(), Path: "kept.go"}); err != nil {
		t.Fatal(err)
	}
}
