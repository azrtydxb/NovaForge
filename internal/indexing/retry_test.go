package indexing_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/indexing"
)

// Only the external model fails here: Git, RPC authorization and both graph
// stores are real. A partial model outage must not become a completed SHA
// that makes Redis redelivery permanently skip the missing search evidence.
func TestPartialIndexFailureRemainsRetryable(t *testing.T) {
	client, impl, root := realGitService(t)
	org := uuid.New()
	repo, err := impl.CreateRepo(scopedCtx(org), &gitv1.CreateRepoRequest{Name: "retry-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatal(err)
	}
	repoID := uuid.MustParse(repo.GetRepo().GetId())
	bare, err := gitops.Open(root, org, repo.GetRepo().GetName())
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	gitCmd(t, "", "clone", "-q", bare.Path(), work)
	sha := commitFiles(t, work, map[string]string{
		"a.go": "package p\nfunc Broken() int { return 1 }\n",
		"b.go": "package p\nfunc Healthy() int { return 2 }\n",
	}, "index retry fixture")
	pool := graphPool(t)
	model := &recoveringEmbedder{fail: true}
	idx := &indexing.Indexer{Git: client, Graph: graph.NewStore(pool), Vectors: graph.NewVectorStore(pool), Embedder: model, HMACSecret: testHMACSecret}
	evt := events.PushEvent{OrgID: org, RepoID: repoID, Ref: "refs/heads/main", OldSHA: zeroSHA, NewSHA: sha}
	ctx := context.Background()
	t.Run("repository lock blocks another replica", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, org.String()+":"+repoID.String()); err != nil {
			t.Fatal(err)
		}
		blocked, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := idx.HandlePush(blocked, evt); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("handler did not wait for repository lock: %v", err)
		}
		if model.calls != 0 {
			t.Fatal("handler indexed while another replica held the lock")
		}
	})
	if err := idx.HandlePush(ctx, evt); err == nil {
		t.Error("partial index reported success; stream consumer would acknowledge it")
	}
	if got := chunkCount(t, pool, scopedCtx(org), org, "b.go"); got == 0 {
		t.Fatal("independent healthy file was not indexed")
	}
	if got := chunkCount(t, pool, scopedCtx(org), org, "a.go"); got != 0 {
		t.Fatal("failed model produced chunks")
	}
	model.fail = false
	if err := idx.HandlePush(ctx, evt); err != nil {
		t.Fatal(err)
	}
	if got := chunkCount(t, pool, scopedCtx(org), org, "a.go"); got == 0 {
		t.Fatal("redelivery skipped the failed file after model recovered")
	}
	calls := model.calls
	if err := idx.HandlePush(ctx, evt); err != nil {
		t.Fatal(err)
	}
	if model.calls != calls {
		t.Fatal("completed redelivery repeated model work")
	}

	// A late delivery of the old event must never resurrect deleted code or
	// replace the current branch with the contents of an older commit.
	if err := os.Remove(filepath.Join(work, "a.go")); err != nil {
		t.Fatal(err)
	}
	newSHA := commitFiles(t, work, map[string]string{"b.go": "package p\nfunc Current() int { return 3 }\n"}, "newer head")
	newEvt := evt
	newEvt.OldSHA, newEvt.NewSHA = sha, newSHA
	if err := idx.HandlePush(ctx, newEvt); err != nil {
		t.Fatal(err)
	}
	if err := idx.HandlePush(ctx, evt); err != nil {
		t.Fatal(err)
	}
	if paths := symbolPaths(t, pool, scopedCtx(org), org); paths["a.go"] {
		t.Fatal("late delivery resurrected deleted a.go")
	}
	var checkpoint string
	if err := pool.QueryRow(ctx, `SELECT attrs->>'sha' FROM graph.graph_nodes WHERE org_id=$1 AND kind='commit' AND key=$2`, org, repoID.String()).Scan(&checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint != newSHA {
		t.Fatalf("late event regressed checkpoint: %s, want %s", checkpoint, newSHA)
	}

	// A newer head can overtake a failed attempt. Its delta alone omits
	// unchanged files and must also clean paths left by the partial attempt.
	partialSHA := commitFiles(t, work, map[string]string{
		"a.go": "package p\nfunc Broken() int { return 4 }\n",
		"z.go": "package p\nfunc Temporary() {}\n",
	}, "partial attempt")
	partialEvt := newEvt
	partialEvt.OldSHA, partialEvt.NewSHA = newSHA, partialSHA
	model.fail = true
	if err := idx.HandlePush(ctx, partialEvt); err == nil {
		t.Fatal("partial attempt unexpectedly succeeded")
	}
	if err := os.Remove(filepath.Join(work, "z.go")); err != nil {
		t.Fatal(err)
	}
	currentSHA := commitFiles(t, work, map[string]string{"c.go": "package p\nfunc Added() int { return 5 }\n"}, "overtake partial attempt")
	currentEvt := partialEvt
	currentEvt.OldSHA, currentEvt.NewSHA = partialSHA, currentSHA
	model.fail = false
	if err := idx.HandlePush(ctx, currentEvt); err != nil {
		t.Fatal(err)
	}
	if err := idx.HandlePush(ctx, partialEvt); err != nil {
		t.Fatal(err)
	}
	paths := symbolPaths(t, pool, scopedCtx(org), org)
	if paths["z.go"] || !paths["a.go"] || !paths["b.go"] || !paths["c.go"] {
		t.Fatalf("reconciliation retained partial/deleted state or lost current files: %v", paths)
	}
	if chunkCount(t, pool, scopedCtx(org), org, "a.go") == 0 {
		t.Fatal("newer event omitted the failed, unchanged file from reconciliation")
	}

	badSHA := commitFiles(t, work, map[string]string{
		"a.go": "package p\nfunc Broken() int { return 6 }\n",
		"z.go": "package p\nfunc Temporary() {}\n",
	}, "partial before force push")
	badEvt := currentEvt
	badEvt.OldSHA, badEvt.NewSHA = currentSHA, badSHA
	model.fail = true
	if err := idx.HandlePush(ctx, badEvt); err == nil {
		t.Fatal("partial attempt unexpectedly succeeded")
	}
	// This is the fixture's disposable repository, not an existing user ref.
	gitCmd(t, work, "reset", "--hard", currentSHA)
	gitCmd(t, work, "push", "-q", "--force", "origin", "HEAD:main")
	rollbackEvt := badEvt
	rollbackEvt.OldSHA, rollbackEvt.NewSHA = badSHA, currentSHA
	model.fail = false
	if err := idx.HandlePush(ctx, rollbackEvt); err != nil {
		t.Fatal(err)
	}
	if paths := symbolPaths(t, pool, scopedCtx(org), org); paths["z.go"] {
		t.Fatal("old completed checkpoint hid partial writes after force push")
	}
}

type recoveringEmbedder struct {
	fail  bool
	calls int
}

func (m *recoveringEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	m.calls++
	for _, text := range texts {
		if m.fail && strings.Contains(text, "Broken") {
			return nil, errors.New("model temporarily unavailable")
		}
	}
	return (stubEmbedder{}).Embed(ctx, texts)
}
