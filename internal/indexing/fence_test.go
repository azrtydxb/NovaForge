package indexing_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/graph"
)

type hookEmbedder struct {
	hook  func()
	calls int
}

func (m *hookEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	m.calls++
	if m.calls == 2 {
		m.hook()
	}
	return (stubEmbedder{}).Embed(ctx, texts)
}

// Terminate only this fixture's advisory-lock owner, never an arbitrary backend.
func killIndexSession(t *testing.T, pool *pgxpool.Pool, org, repo uuid.UUID) {
	t.Helper()
	var pid int
	err := pool.QueryRow(context.Background(), `SELECT pid FROM pg_locks
 WHERE locktype='advisory' AND granted AND objsubid=1
 AND database=(SELECT oid FROM pg_database WHERE datname=current_database())
 AND classid = ((hashtextextended($1,0) >> 32) & 4294967295)::oid
 AND objid = (hashtextextended($1,0) & 4294967295)::oid`, org.String()+":"+repo.String()).Scan(&pid)
	if err != nil {
		t.Fatal(err)
	}
	var killed bool
	if err := pool.QueryRow(context.Background(), `SELECT pg_terminate_backend($1)`, pid).Scan(&killed); err != nil || !killed {
		t.Fatalf("kill own backend: %v, %v", killed, err)
	}
}

func TestIndexSessionLossFencesEveryWrite(t *testing.T) {
	for _, purge := range []bool{false, true} {
		t.Run(fmt.Sprintf("purge=%v", purge), func(t *testing.T) {
			git := newFakeGitClient()
			org, repo := uuid.New(), uuid.New()
			git.head = "head"
			for _, p := range []string{"a.go", "b.go", "c.go"} {
				git.blobs[p+"@head"] = []byte(goSrc)
				git.diffs["4b825dc642cb6eb9a060e54bf8d69288fbee4904..head"] += "diff --git a/" + p + " b/" + p + "\n"
			}
			idx := newIndexer(t, git)
			idx.HMACSecret = testHMACSecret
			model := &hookEmbedder{}
			model.hook = func() {
				killIndexSession(t, idx.Graph.Pool(), org, repo)
				if chunkCount(t, idx.Graph.Pool(), scopedCtx(org), org, "a.go") != 1 {
					t.Error("healthy file lost before session failure")
				}
				if purge {
					if err := idx.Graph.PurgeRepository(scopedCtx(org), repo); err != nil {
						t.Fatal(err)
					}
				}
			}
			idx.Embedder = model
			err := idx.HandlePush(context.Background(), events.PushEvent{OrgID: org, RepoID: repo, Ref: "refs/heads/main", NewSHA: "head"})
			if err == nil {
				t.Error("dead lock owner reported completed index")
			}
			if n := chunkCount(t, idx.Graph.Pool(), scopedCtx(org), org, "b.go"); n != 0 {
				t.Errorf("dead owner wrote %d chunks after losing session", n)
			}
			paths := symbolPaths(t, idx.Graph.Pool(), scopedCtx(org), org)
			if paths["c.go"] {
				t.Error("dead owner wrote next file's graph")
			}
			var checkpoints int
			if err := idx.Graph.Pool().QueryRow(context.Background(), `SELECT count(*) FROM graph.graph_nodes WHERE org_id=$1 AND key=$2 AND attrs ? 'sha'`, org, repo.String()).Scan(&checkpoints); err != nil {
				t.Fatal(err)
			}
			if checkpoints != 0 {
				t.Error("dead owner wrote checkpoint")
			}
			if !purge && chunkCount(t, idx.Graph.Pool(), scopedCtx(org), org, "a.go") != 1 {
				t.Error("healthy-file partial progress rolled back")
			}
			if purge {
				idx.Embedder = stubEmbedder{}
				if err := idx.HandlePush(context.Background(), events.PushEvent{OrgID: org, RepoID: repo, Ref: "refs/heads/main", NewSHA: "head"}); err != nil {
					t.Fatal(err)
				}
				if paths := symbolPaths(t, idx.Graph.Pool(), scopedCtx(org), org); len(paths) != 0 {
					t.Errorf("queued stale event resurrected deleted repo: %v", paths)
				}
			}
		})
	}
}

func TestPurgeWaitsForInFlightIndex(t *testing.T) {
	git := newFakeGitClient()
	git.head = "head"
	org, repo := uuid.New(), uuid.New()
	for _, p := range []string{"a.go", "b.go"} {
		git.blobs[p+"@head"] = []byte(goSrc)
		git.diffs["4b825dc642cb6eb9a060e54bf8d69288fbee4904..head"] += "diff --git a/" + p + " b/" + p + "\n"
	}
	idx := newIndexer(t, git)
	idx.HMACSecret = testHMACSecret
	idx.Embedder = &hookEmbedder{hook: func() {
		ctx, cancel := context.WithTimeout(scopedCtx(org), 200*time.Millisecond)
		defer cancel()
		if err := idx.Graph.PurgeRepository(ctx, repo); err == nil {
			t.Error("purge passed a live indexing session")
		}
	}}
	if err := idx.HandlePush(context.Background(), events.PushEvent{OrgID: org, RepoID: repo, Ref: "refs/heads/main", NewSHA: "head"}); err != nil {
		t.Fatal(err)
	}
	if err := idx.Graph.PurgeRepository(scopedCtx(org), repo); err != nil {
		t.Fatal(err)
	}
	if err := idx.Graph.ReplaceFileIndex(scopedCtx(org), graph.FileIndex{OrgID: org, RepoID: repo, Path: "late.go"}); err == nil {
		t.Error("direct late graph writer resurrected purged repo")
	}
	if err := idx.Vectors.Upsert(scopedCtx(org), org, repo, "late.go", nil); err == nil {
		t.Error("direct late vector writer ignored deletion")
	}
}

func TestIndexUsesOnlyItsFencedConnection(t *testing.T) {
	git := newFakeGitClient()
	git.head = "head"
	git.blobs["a.go@head"] = []byte(goSrc)
	git.diffs["4b825dc642cb6eb9a060e54bf8d69288fbee4904..head"] = "diff --git a/a.go b/a.go\n"
	idx := newIndexer(t, git)
	idx.HMACSecret = testHMACSecret
	cfg, err := pgxpool.ParseConfig(dbURL(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	idx.Graph = graph.NewStore(pool)
	idx.Vectors = graph.NewVectorStore(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := idx.HandlePush(ctx, events.PushEvent{OrgID: uuid.New(), RepoID: uuid.New(), Ref: "refs/heads/main", NewSHA: "head"}); err != nil {
		t.Fatal(err)
	}
}

func TestLostWriterCannotOverwriteReplacementWorker(t *testing.T) {
	git := newFakeGitClient()
	org, repo := uuid.New(), uuid.New()
	git.head = "old"
	for _, p := range []string{"a.go", "b.go", "c.go"} {
		git.diffs["4b825dc642cb6eb9a060e54bf8d69288fbee4904..old"] += "diff --git a/" + p + " b/" + p + "\n"
		if p != "c.go" {
			git.blobs[p+"@old"] = []byte(goSrc)
		} // stale delete must not remove the winner's c.go
		git.blobs[p+"@new"] = []byte("package p\nfunc Winner() {}\n")
		git.diffs["4b825dc642cb6eb9a060e54bf8d69288fbee4904..new"] += "diff --git a/" + p + " b/" + p + "\n"
	}
	idx := newIndexer(t, git)
	idx.HMACSecret = testHMACSecret
	evt := events.PushEvent{OrgID: org, RepoID: repo, Ref: "refs/heads/main", NewSHA: "old"}
	idx.Embedder = &hookEmbedder{hook: func() {
		killIndexSession(t, idx.Graph.Pool(), org, repo)
		git.head = "new"
		winner := *idx
		winner.Embedder = stubEmbedder{}
		if err := winner.HandlePush(context.Background(), evt); err != nil {
			t.Fatal(err)
		}
	}}
	if err := idx.HandlePush(context.Background(), evt); err == nil {
		t.Fatal("lost worker reported success")
	}
	var sha string
	if err := idx.Graph.Pool().QueryRow(context.Background(), `SELECT attrs->>'sha' FROM graph.graph_nodes WHERE org_id=$1 AND key=$2`, org, repo.String()).Scan(&sha); err != nil {
		t.Fatal(err)
	}
	if sha != "new" {
		t.Fatalf("losing worker regressed checkpoint to %s", sha)
	}
	var symbols int
	if err := idx.Graph.Pool().QueryRow(context.Background(), `SELECT count(*) FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND kind='symbol' AND attrs->>'name'='Winner'`, org, repo).Scan(&symbols); err != nil {
		t.Fatal(err)
	}
	if symbols != 3 {
		t.Fatalf("stale writer/deindexer corrupted winner: %d symbols", symbols)
	}
	var chunks int
	if err := idx.Graph.Pool().QueryRow(context.Background(), `SELECT count(*) FROM graph.code_chunks WHERE org_id=$1 AND repo_id=$2 AND text LIKE '%Winner%'`, org, repo).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if chunks != 3 {
		t.Fatalf("stale writer corrupted winner: %d chunks", chunks)
	}
}
