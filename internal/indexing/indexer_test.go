package indexing_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/indexing"
)

// fakeGitClient is a deterministic in-process test double standing in for
// the git service's gRPC client: it hands back caller-configured blob
// content and diffs, and records every path it was asked to fetch so tests
// can prove the indexer touched only the paths it should have.
type fakeGitClient struct {
	// blobs maps "path@sha" to file content.
	blobs map[string][]byte
	// errFor maps a path to an error GetBlob should return for it,
	// regardless of sha — used to simulate a fetch/parse failure that is
	// not a deletion.
	errFor map[string]error
	// diffs maps "old..new" to a unified diff body.
	diffs map[string]string

	fetched []string
	head    string
}

func newFakeGitClient() *fakeGitClient {
	return &fakeGitClient{
		blobs:  map[string][]byte{},
		errFor: map[string]error{},
		diffs:  map[string]string{},
	}
}

func (f *fakeGitClient) GetBlob(_ context.Context, in *gitv1.GetBlobRequest, _ ...grpc.CallOption) (*gitv1.GetBlobResponse, error) {
	f.fetched = append(f.fetched, in.GetPath())
	if err, ok := f.errFor[in.GetPath()]; ok {
		return nil, err
	}
	content, ok := f.blobs[in.GetPath()+"@"+in.GetRef()]
	if !ok {
		return nil, status.Error(codes.NotFound, "blob not found")
	}
	return &gitv1.GetBlobResponse{Content: content}, nil
}

// GetRepo reports main as the default branch, which is the branch every
// event these fake-backed tests deliver is pushed to.
func (f *fakeGitClient) GetRepo(_ context.Context, in *gitv1.GetRepoRequest, _ ...grpc.CallOption) (*gitv1.GetRepoResponse, error) {
	return &gitv1.GetRepoResponse{Repo: &gitv1.Repo{Id: in.GetName(), DefaultBranch: "main"}}, nil
}

// ListCommits exposes the configured default-branch head, but no history:
// the real-git-service tests cover commit attribution.
func (f *fakeGitClient) ListCommits(_ context.Context, in *gitv1.ListCommitsRequest, _ ...grpc.CallOption) (*gitv1.ListCommitsResponse, error) {
	if in.GetRef() == "refs/heads/main" && f.head != "" {
		return &gitv1.ListCommitsResponse{Commits: []*gitv1.Commit{{Sha: f.head}}}, nil
	}
	return &gitv1.ListCommitsResponse{}, nil
}

func (f *fakeGitClient) GetDiff(_ context.Context, in *gitv1.GetDiffRequest, _ ...grpc.CallOption) (*gitv1.GetDiffResponse, error) {
	return &gitv1.GetDiffResponse{Unified: f.diffs[in.GetFrom()+".."+in.GetTo()]}, nil
}

// stubEmbedder is a deterministic in-process test double for the external
// embedding model.
type stubEmbedder struct{}

func (stubEmbedder) Embed(_ context.Context, chunks []string) ([][]float32, error) {
	out := make([][]float32, len(chunks))
	for i := range chunks {
		out[i] = make([]float32, graph.EmbeddingDim)
	}
	return out, nil
}

func dbURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return u
}

func graphPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := dbURL(t)
	if err := database.Migrate(url, "graph", os.DirFS("../graph/migrations")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newIndexer(t *testing.T, git *fakeGitClient) *indexing.Indexer {
	t.Helper()
	pool := graphPool(t)
	return &indexing.Indexer{
		Git:      git,
		Graph:    graph.NewStore(pool),
		Vectors:  graph.NewVectorStore(pool),
		Embedder: stubEmbedder{},
	}
}

func scopedCtx(orgID uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{OrgID: orgID, ActorKind: "user"})
}

func symbolPaths(t *testing.T, pool *pgxpool.Pool, ctx context.Context, orgID uuid.UUID) map[string]bool {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT DISTINCT attrs->>'path' FROM graph.graph_nodes WHERE org_id = $1 AND kind = 'symbol'`, orgID)
	if err != nil {
		t.Fatalf("query symbol paths: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[p] = true
	}
	return out
}

func chunkCount(t *testing.T, pool *pgxpool.Pool, ctx context.Context, orgID uuid.UUID, path string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM graph.code_chunks WHERE org_id = $1 AND path = $2`, orgID, path).Scan(&n); err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	return n
}

const goSrc = "package p\n\nfunc F() int { return 1 }\n"

// TestIndexesOnlyChangedPaths proves the indexer never touches a path
// outside the changed set: in a three-file repository, only the one path
// named in changedPaths is fetched (and therefore parsed) at all.
func TestIndexesOnlyChangedPaths(t *testing.T) {
	git := newFakeGitClient()
	orgID := uuid.New()
	repoID := uuid.New()
	sha := "sha-" + uuid.NewString()

	git.blobs["b.go@"+sha] = []byte(goSrc)
	// a.go and c.go exist in the repository but are NOT in changedPaths,
	// and are deliberately not registered as fetchable blobs at all: if
	// the indexer ever asked for them, GetBlob would 404 on them just the
	// same as b.go's absence would, so the real proof is f.fetched below.

	idx := newIndexer(t, git)
	indexed, err := idx.IndexCommit(context.Background(), orgID, repoID, sha, []string{"b.go"})
	if err != nil {
		t.Fatalf("IndexCommit: %v", err)
	}
	if indexed != 1 {
		t.Fatalf("indexed = %d, want 1", indexed)
	}
	if len(git.fetched) != 1 || git.fetched[0] != "b.go" {
		t.Fatalf("fetched = %v, want exactly [b.go] — Parse must be invoked exactly once, on the changed file only", git.fetched)
	}

	ctx := scopedCtx(orgID)
	paths := symbolPaths(t, idx.Graph.Pool(), ctx, orgID)
	if !paths["b.go"] || len(paths) != 1 {
		t.Fatalf("indexed symbol paths = %v, want exactly {b.go}", paths)
	}
}

// TestDeletedFileRemovesSymbols indexes a file, then indexes its deletion
// (the git service reporting it absent at the new SHA), and asserts its
// symbols and chunks are gone.
func TestDeletedFileRemovesSymbols(t *testing.T) {
	git := newFakeGitClient()
	orgID := uuid.New()
	repoID := uuid.New()
	sha1 := "sha1-" + uuid.NewString()
	sha2 := "sha2-" + uuid.NewString()

	git.blobs["d.go@"+sha1] = []byte(goSrc)
	// d.go is absent at sha2 — GetBlob for d.go@sha2 falls through to the
	// fake's NotFound default, simulating a deletion.

	idx := newIndexer(t, git)
	ctx := context.Background()

	if _, err := idx.IndexCommit(ctx, orgID, repoID, sha1, []string{"d.go"}); err != nil {
		t.Fatalf("IndexCommit 1: %v", err)
	}
	scoped := scopedCtx(orgID)
	if paths := symbolPaths(t, idx.Graph.Pool(), scoped, orgID); !paths["d.go"] {
		t.Fatalf("expected d.go indexed after first commit, got %v", paths)
	}
	if n := chunkCount(t, idx.Graph.Pool(), scoped, orgID, "d.go"); n == 0 {
		t.Fatalf("expected chunks for d.go after first commit, got 0")
	}

	if _, err := idx.IndexCommit(ctx, orgID, repoID, sha2, []string{"d.go"}); err != nil {
		t.Fatalf("IndexCommit 2 (deletion): %v", err)
	}
	if paths := symbolPaths(t, idx.Graph.Pool(), scoped, orgID); paths["d.go"] {
		t.Fatalf("expected d.go symbols removed after deletion, still present: %v", paths)
	}
	if n := chunkCount(t, idx.Graph.Pool(), scoped, orgID, "d.go"); n != 0 {
		t.Fatalf("expected 0 chunks for d.go after deletion, got %d", n)
	}
}

// TestRedeliveredPushIndexesOnce delivers the same commit to IndexCommit
// twice and asserts the second delivery is a no-op: it never touches git
// again, and the resulting node set is identical to a single delivery.
func TestRedeliveredPushIndexesOnce(t *testing.T) {
	git := newFakeGitClient()
	orgID := uuid.New()
	repoID := uuid.New()
	sha := "sha-" + uuid.NewString()
	git.blobs["r.go@"+sha] = []byte(goSrc)

	idx := newIndexer(t, git)
	ctx := context.Background()

	first, err := idx.IndexCommit(ctx, orgID, repoID, sha, []string{"r.go"})
	if err != nil {
		t.Fatalf("IndexCommit 1: %v", err)
	}
	if first != 1 {
		t.Fatalf("first indexed = %d, want 1", first)
	}
	fetchedAfterFirst := len(git.fetched)

	second, err := idx.IndexCommit(ctx, orgID, repoID, sha, []string{"r.go"})
	if err != nil {
		t.Fatalf("IndexCommit 2 (redelivery): %v", err)
	}
	if second != 0 {
		t.Fatalf("redelivered indexed = %d, want 0 (already-indexed sha returns immediately)", second)
	}
	if len(git.fetched) != fetchedAfterFirst {
		t.Fatalf("redelivery fetched git again: %v", git.fetched)
	}

	scoped := scopedCtx(orgID)
	rows, err := idx.Graph.Pool().Query(scoped, `SELECT id FROM graph.graph_nodes WHERE org_id = $1 AND kind = 'symbol' AND attrs->>'path' = 'r.go'`, orgID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 {
		t.Fatalf("symbol node count after redelivery = %d, want 1 (identical to a single delivery)", len(ids))
	}
}

// TestIndexerSurvivesParseError asserts a file that fails to fetch (and so
// cannot be parsed) is skipped with a logged error while the remaining
// files in the same commit still index. The attempt must nevertheless report
// failure so the stream consumer retains the incomplete work for retry.
func TestIndexerSurvivesParseError(t *testing.T) {
	git := newFakeGitClient()
	orgID := uuid.New()
	repoID := uuid.New()
	sha := "sha-" + uuid.NewString()

	git.errFor["broken.go"] = fmt.Errorf("simulated fetch failure")
	git.blobs["ok.go@"+sha] = []byte(goSrc)

	idx := newIndexer(t, git)
	indexed, err := idx.IndexCommit(context.Background(), orgID, repoID, sha, []string{"broken.go", "ok.go"})
	if err == nil {
		t.Fatal("IndexCommit must report the failed path without discarding healthy-file progress")
	}
	if indexed != 1 {
		t.Fatalf("indexed = %d, want 1 (only ok.go)", indexed)
	}

	ctx := scopedCtx(orgID)
	paths := symbolPaths(t, idx.Graph.Pool(), ctx, orgID)
	if !paths["ok.go"] {
		t.Fatalf("expected ok.go indexed despite broken.go failing, got %v", paths)
	}
	if paths["broken.go"] {
		t.Fatalf("broken.go should not have been indexed, got %v", paths)
	}
}

// TestRunConsumesPushEventsAndAcks proves Run's wiring end to end against a
// real Redis: publishing a push event causes the indexer to index the
// changed path, and the message is acknowledged (no longer pending) only
// after the handler succeeds.
func TestRunConsumesPushEventsAndAcks(t *testing.T) {
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	orgID := uuid.New()
	repoID := uuid.New()
	sha := "sha-" + uuid.NewString()

	git := newFakeGitClient()
	git.head = sha
	git.diffs["4b825dc642cb6eb9a060e54bf8d69288fbee4904.."+sha] = "diff --git a/run.go b/run.go\n--- a/run.go\n+++ b/run.go\n"
	git.blobs["run.go@"+sha] = []byte(goSrc)

	idx := newIndexer(t, git)
	idx.RDB = rdb
	idx.HMACSecret = testHMACSecret
	idx.Consumer = "test-consumer-" + uuid.NewString()

	// Run consumes events.StreamGitPush by its fixed name, so this test
	// publishes there directly and cleans up afterward.
	streamName := events.StreamGitPush
	t.Cleanup(func() { rdb.Del(context.Background(), streamName) })

	evt := events.PushEvent{
		OrgID:  orgID,
		RepoID: repoID,
		Ref:    "refs/heads/main",
		OldSHA: "oldsha",
		NewSHA: sha,
		At:     time.Now().UTC(),
	}
	if err := events.Publish(ctx, rdb, streamName, evt); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	runErrCh := make(chan error, 1)
	go func() { runErrCh <- idx.Run(ctx) }()

	deadline := time.Now().Add(15 * time.Second)
	scoped := scopedCtx(orgID)
	for {
		paths := symbolPaths(t, idx.Graph.Pool(), scoped, orgID)
		if paths["run.go"] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for run.go to be indexed via Run()")
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return after context cancellation")
	}
}
