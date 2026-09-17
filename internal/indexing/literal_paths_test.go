package indexing_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/indexing"
	"google.golang.org/grpc"
)

func TestPushIndexesAndRemovesLiteralGitPaths(t *testing.T) {
	client, impl, root := realGitService(t)
	org := uuid.New()
	repo, err := impl.CreateRepo(scopedCtx(org), &gitv1.CreateRepoRequest{Name: "paths-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatal(err)
	}
	repoID := uuid.MustParse(repo.GetRepo().GetId())
	bare, err := gitops.Open(root, org, repo.GetRepo().GetName())
	if err != nil {
		t.Fatal(err)
	}
	gitCmd(t, "", "--git-dir="+bare.Path(), "config", "diff.renames", "true")
	gitCmd(t, "", "--git-dir="+bare.Path(), "config", "diff.noprefix", "true")
	work := t.TempDir()
	gitCmd(t, "", "clone", "-q", bare.Path(), work)
	files := map[string]string{}
	for _, name := range []string{"space name.go", "tab\tname.go", "line\nname.go", "café.go", "quote\"name.go"} {
		files[name] = "package p\nfunc Literal() {}\n"
	}
	sha := commitFiles(t, work, files, "literal path fixture")
	pool := graphPool(t)
	idx := &indexing.Indexer{Git: client, Graph: graph.NewStore(pool), Vectors: graph.NewVectorStore(pool), Embedder: stubEmbedder{}, HMACSecret: testHMACSecret}
	evt := events.PushEvent{OrgID: org, RepoID: repoID, Ref: "refs/heads/main", NewSHA: sha}
	ctx := context.Background()
	idx.Git = legacyDiffClient{client}
	if err := idx.HandlePush(ctx, evt); err == nil {
		t.Fatal("legacy server's absent manifest was checkpointed as an empty change")
	}
	idx.Git = client
	if err := idx.HandlePush(ctx, evt); err != nil {
		t.Fatal(err)
	}
	for name := range files {
		if n := chunkCount(t, pool, scopedCtx(org), org, name); n == 0 {
			t.Errorf("push omitted literal path %q", name)
		}
		var attributed bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM graph.graph_nodes n JOIN graph.graph_edges e ON e.from_id=n.id AND e.kind='changed_by' WHERE n.org_id=$1 AND n.repo_id=$2 AND n.kind='symbol' AND n.attrs->>'path'=$3)`, org, repoID, name).Scan(&attributed); err != nil {
			t.Fatal(err)
		}
		if !attributed {
			t.Errorf("literal path lost symbol change evidence: %q", name)
		}
	}
	// A rename must de-index the old path, not merely add the new one.
	if err := os.Rename(filepath.Join(work, "space name.go"), filepath.Join(work, "renamed.go")); err != nil {
		t.Fatal(err)
	}
	evt.OldSHA = sha
	evt.NewSHA = commitFiles(t, work, map[string]string{}, "rename literal path")
	if err := idx.HandlePush(ctx, evt); err != nil {
		t.Fatal(err)
	}
	if n := chunkCount(t, pool, scopedCtx(org), org, "space name.go"); n != 0 {
		t.Fatal("rename left old source in the index")
	}
	if n := chunkCount(t, pool, scopedCtx(org), org, "renamed.go"); n == 0 {
		t.Fatal("rename omitted new source")
	}
}

// A rolling upgrade can temporarily pair the new indexer with an old Git
// service. Unknown path completeness must remain retryable, not mean no files.
type legacyDiffClient struct{ gitv1.GitServiceClient }

func (c legacyDiffClient) GetDiff(ctx context.Context, req *gitv1.GetDiffRequest, opts ...grpc.CallOption) (*gitv1.GetDiffResponse, error) {
	resp, err := c.GitServiceClient.GetDiff(ctx, req, opts...)
	if resp != nil {
		resp.PathsComplete = false
		resp.ChangedPaths = nil
	}
	return resp, err
}
