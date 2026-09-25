package graph_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/graph"
)

func TestOrganizationPurgeFencesAllRepositories(t *testing.T) {
	store := newStore(t)
	org, other := uuid.New(), uuid.New()
	repo1, repo2 := uuid.New(), uuid.New()
	ctx := scopedCtx(org, uuid.New())
	one, release1, err := store.LockIndex(ctx, org, repo1)
	if err != nil {
		t.Fatal(err)
	}
	defer release1()
	_, release2, err := store.LockIndex(ctx, org, repo2)
	if err != nil {
		t.Fatal(err)
	}
	defer release2()
	if err := store.SetIndexCheckpoint(one, org, repo1, "sha", "version"); err != nil {
		t.Fatal(err)
	}
	timed, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	if err := store.PurgeOrganization(timed); err == nil {
		t.Error("organization purge passed two active repositories")
	}
	cancel()
	release1()
	timed, cancel = context.WithTimeout(ctx, 150*time.Millisecond)
	if err := store.PurgeOrganization(timed); err == nil {
		t.Error("organization purge passed remaining active repository")
	}
	cancel()
	release2()
	if err := store.PurgeOrganization(ctx); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []uuid.UUID{repo1, repo2, uuid.New()} {
		if _, release, err := store.LockIndex(ctx, org, repo); !errors.Is(err, graph.ErrIndexDeleted) {
			if release != nil {
				release()
			}
			t.Errorf("late org repository lock: %v", err)
		}
		if err := store.ReplaceFileIndex(ctx, graph.FileIndex{OrgID: org, RepoID: repo, Path: "late.go"}); !errors.Is(err, graph.ErrIndexDeleted) {
			t.Errorf("late graph write: %v", err)
		}
		if err := graph.NewVectorStore(store.Pool()).Upsert(ctx, org, repo, "late.go", nil); !errors.Is(err, graph.ErrIndexDeleted) {
			t.Errorf("late vector write: %v", err)
		}
		if err := store.SetIndexCheckpoint(ctx, org, repo, "late", "version"); !errors.Is(err, graph.ErrIndexDeleted) {
			t.Errorf("late checkpoint: %v", err)
		}
	}
	if _, err := store.UpsertNode(ctx, graph.Node{OrgID: org, Kind: "service", Key: "late"}); !errors.Is(err, graph.ErrIndexDeleted) {
		t.Errorf("late generic org node: %v", err)
	}
	if err := store.ReplaceFileIndex(scopedCtx(other, uuid.New()), graph.FileIndex{OrgID: other, RepoID: repo1, Path: "kept.go"}); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledIndexSessionReleasesLocks(t *testing.T) {
	store := newStore(t)
	org, repo := uuid.New(), uuid.New()
	scoped := scopedCtx(org, uuid.New())
	ctx, cancel := context.WithCancel(scoped)
	_, release, err := store.LockIndex(ctx, org, repo)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	release()
	bounded, done := context.WithTimeout(scoped, time.Second)
	defer done()
	if err := store.PurgeOrganization(bounded); err != nil {
		t.Fatalf("cancelled owner leaked locks: %v", err)
	}
}

func TestGraphFencesRejectUnscopedAndMismatchedWriters(t *testing.T) {
	store := newStore(t)
	org, repo := uuid.New(), uuid.New()
	ctx := scopedCtx(org, uuid.New())
	if _, release, err := store.LockIndex(context.Background(), org, repo); err == nil {
		release()
		t.Fatal("unscoped lock")
	}
	platform := authz.WithScope(context.Background(), authz.Scope{ActorKind: "service", PlatformWorker: "test"})
	if _, release, err := store.LockIndex(platform, uuid.Nil, repo); err == nil {
		release()
		t.Fatal("platform-scoped lock")
	}
	locked, release, err := store.LockIndex(ctx, org, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := store.ReplaceFileIndex(locked, graph.FileIndex{OrgID: org, RepoID: uuid.New(), Path: "wrong.go"}); err == nil {
		t.Fatal("session crossed repository")
	}
	if err := store.ReplaceFileIndex(authz.WithScope(locked, authz.Scope{OrgID: uuid.New()}), graph.FileIndex{OrgID: org, RepoID: repo, Path: "wrong.go"}); err == nil {
		t.Fatal("session crossed organization")
	}
	if err := store.PurgeRepository(ctx, uuid.Nil); err == nil {
		t.Fatal("nil repository became org tombstone")
	}
}

func TestCancelledIndexLockWaitReleasesOrganizationLock(t *testing.T) {
	store := newStore(t)
	org, repo := uuid.New(), uuid.New()
	scoped := scopedCtx(org, uuid.New())
	tx, err := store.Pool().Begin(scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(scoped) //nolint:errcheck
	if _, err := tx.Exec(scoped, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, org.String()+":"+repo.String()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(scoped, 150*time.Millisecond)
	_, release, err := store.LockIndex(ctx, org, repo)
	cancel()
	if err == nil {
		release()
		t.Fatal("did not wait for repository lock")
	}
	if err := tx.Rollback(scoped); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(scoped, time.Second)
	defer cancel()
	if err := store.PurgeOrganization(ctx); err != nil {
		t.Fatalf("failed acquisition leaked shared org lock: %v", err)
	}
}

func TestReleasedIndexContextCannotWrite(t *testing.T) {
	store := newStore(t)
	org, repo := uuid.New(), uuid.New()
	scoped := scopedCtx(org, uuid.New())
	ctx, release, err := store.LockIndex(scoped, org, repo)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := store.SetIndexCheckpoint(ctx, org, repo, "stale", "version"); err == nil {
		t.Fatal("released context wrote checkpoint")
	}
	if _, err := store.IndexPaths(ctx, org, repo); err == nil {
		t.Fatal("released context read through pooled session")
	}
}
