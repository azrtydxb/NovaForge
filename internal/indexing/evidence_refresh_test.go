package indexing_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/indexing"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestModuleChangesRefreshUnchangedGoEvidence(t *testing.T) {
	client, impl, root := realGitService(t)
	org := uuid.New()
	repo, err := impl.CreateRepo(scopedCtx(org), &gitv1.CreateRepoRequest{Name: "evidence-" + uuid.NewString()[:8]})
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
	sha := commitFiles(t, work, map[string]string{"go.mod": "module example.com/old\n", "a.go": "package p\nfunc local() {}\n"}, "old module")
	pool := graphPool(t)
	idx := &indexing.Indexer{Git: client, Graph: graph.NewStore(pool), Vectors: graph.NewVectorStore(pool), Embedder: stubEmbedder{}, HMACSecret: testHMACSecret}
	evt := events.PushEvent{OrgID: org, RepoID: repoID, Ref: "refs/heads/main", NewSHA: sha}
	ctx := context.Background()
	if err := idx.HandlePush(ctx, evt); err != nil {
		t.Fatal(err)
	}
	t.Run("legacy checkpoint is not extraction evidence", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE graph.graph_nodes SET attrs=attrs-'source_hash' WHERE org_id=$1 AND repo_id=$2 AND kind='file'`, org, repoID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE graph.graph_nodes SET attrs=attrs-'extraction_version' WHERE org_id=$1 AND kind='commit' AND key=$2`, org, repoID.String()); err != nil {
			t.Fatal(err)
		}
		if err := idx.HandlePush(ctx, evt); err != nil {
			t.Fatal(err)
		}
		var refreshed bool
		if err := pool.QueryRow(ctx, `SELECT attrs ? 'source_hash' FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND kind='file' AND attrs->>'path'='a.go'`, org, repoID).Scan(&refreshed); err != nil {
			t.Fatal(err)
		}
		if !refreshed {
			t.Fatal("matching legacy SHA suppressed extraction-evidence refresh")
		}
	})
	module := "module example.com/new\n"
	evt.OldSHA = sha
	evt.NewSHA = commitFiles(t, work, map[string]string{"go.mod": module}, "remap module without touching Go source")
	if err := idx.HandlePush(ctx, evt); err != nil {
		t.Fatal(err)
	}
	var digest string
	if err := pool.QueryRow(ctx, `SELECT attrs->>'module_hash' FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND kind='file' AND attrs->>'path'='a.go'`, org, repoID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != graph.SourceDigest([]byte(module)) {
		t.Fatalf("unchanged Go source retained stale module evidence: %s", digest)
	}
	evt.OldSHA = evt.NewSHA
	evt.NewSHA = commitFiles(t, work, map[string]string{"a.go": "package p\nfunc updated() {}\n"}, "module RPC failure fixture")
	idx.Git = &moduleFaultClient{GitServiceClient: client}
	if err := idx.HandlePush(ctx, evt); err == nil {
		t.Fatal("module read failure checkpointed unknown extraction context")
	}
	idx.Git = client
	if err := idx.HandlePush(ctx, evt); err != nil {
		t.Fatalf("module read recovery: %v", err)
	}
}

// Fail only the first module lookup, not source reads. Otherwise the source
// failure would mask a missing guard around import-resolution context.
type moduleFaultClient struct {
	gitv1.GitServiceClient
	failed bool
}

func (c *moduleFaultClient) GetBlob(ctx context.Context, req *gitv1.GetBlobRequest, opts ...grpc.CallOption) (*gitv1.GetBlobResponse, error) {
	if req.GetPath() == "go.mod" && !c.failed {
		c.failed = true
		return nil, status.Error(codes.Unavailable, "module lookup interrupted")
	}
	return c.GitServiceClient.GetBlob(ctx, req, opts...)
}
