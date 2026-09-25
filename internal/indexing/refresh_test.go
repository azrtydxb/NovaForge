package indexing_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/indexing"
	"github.com/redis/go-redis/v9"
)

func TestRunRefreshesLegacyEvidenceWithoutPush(t *testing.T) {
	client, impl, root := realGitService(t)
	org := uuid.New()
	repo, err := impl.CreateRepo(scopedCtx(org), &gitv1.CreateRepoRequest{Name: "refresh-" + uuid.NewString()[:8]})
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
	sha := commitFiles(t, work, map[string]string{"go.mod": "module example.com/refresh\n", "a.go": goSrc}, "startup refresh")
	pool := graphPool(t)
	store := graph.NewStore(pool)
	if _, err := store.UpsertNode(scopedCtx(org), graph.Node{OrgID: org, Kind: "commit", Key: repoID.String(), Attrs: map[string]string{"sha": sha}}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	stream := "stream:test:refresh:" + uuid.NewString()
	defer rdb.Del(context.Background(), stream)
	idx := &indexing.Indexer{RDB: rdb, Git: client, Graph: store, Vectors: graph.NewVectorStore(pool), Embedder: stubEmbedder{}, HMACSecret: testHMACSecret, PushStream: stream}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- idx.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not cancel refresh")
		}
	}()
	// What is asserted is that startup refreshes legacy evidence at all, without
	// a push — not how quickly. The bound was 6 seconds, which this refresh has
	// taken on its own against the cluster's database, so under the rest of the
	// suite it failed for being slow rather than for never happening. A generous
	// bound costs nothing when the refresh works and still fails when it does not.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var valid bool
		err := pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND kind='file' AND attrs->>'path'='a.go' AND attrs->>'source_hash'=$3) AND EXISTS(SELECT 1 FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND key=$2::text AND attrs->>'sha'=$4 AND attrs ? 'extraction_version')`, org, repoID, graph.SourceDigest([]byte(goSrc)), sha).Scan(&valid)
		if err != nil {
			t.Fatal(err)
		}
		if valid {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("startup did not refresh legacy evidence without a push")
}
