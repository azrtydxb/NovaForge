package indexing_test

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/indexing"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// zeroSHA is what git reports as the old value of a ref that did not exist
// before the push: every repository's first push carries it.
const zeroSHA = "0000000000000000000000000000000000000000"

const testHMACSecret = "indexing-test-secret"

// realGitService serves the real git service over a real gRPC connection,
// behind the same credential interceptor the platform's services use, so a
// call that carries no credential is refused here exactly as it is on the
// cluster. The in-process fake the other indexer tests use accepts anything,
// which is how an indexer that never authenticated passed all of them.
func realGitService(t *testing.T) (gitv1.GitServiceClient, *gitops.Server, string) {
	t.Helper()
	url := dbURL(t)
	if err := database.MigrateAs(url, "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform schema: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	root := t.TempDir()
	impl := gitops.NewGRPCServer(pool, root)
	srv := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(nil, testHMACSecret)))
	gitv1.RegisterGitServiceServer(srv, impl)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return gitv1.NewGitServiceClient(conn), impl, root
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v — %s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(out.String())
}

func commitFiles(t *testing.T, work string, files map[string]string, msg string) string {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(work, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	gitCmd(t, work, "add", "-A")
	gitCmd(t, work, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-q", "-m", msg)
	gitCmd(t, work, "push", "-q", "origin", "HEAD:main")
	return gitCmd(t, work, "rev-parse", "HEAD")
}

// TestFirstPushIsIndexedThroughTheRealGitService drives a repository's first
// push, and then a second one, from the push event to stored symbols and
// chunks, against the real git service.
//
// Two defects made this path index nothing on the cluster. The indexer called
// the git service with no credential, and the git service — correctly —
// refused an unauthenticated caller. And a first push carries an all-zero old
// SHA, which `git diff` cannot resolve, so the event failed, was never
// acknowledged, and was retried forever.
func TestFirstPushIsIndexedThroughTheRealGitService(t *testing.T) {
	gitClient, impl, root := realGitService(t)
	orgID := uuid.New()

	created, err := impl.CreateRepo(scopedCtx(orgID), &gitv1.CreateRepoRequest{Name: "indexed-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	repoID := uuid.MustParse(created.GetRepo().GetId())
	bare, err := gitops.Open(root, orgID, created.GetRepo().GetName())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	work := t.TempDir()
	gitCmd(t, "", "clone", "-q", bare.Path(), work)
	first := commitFiles(t, work, map[string]string{
		"billing/vat.go": "package billing\n\nfunc VATTotal(net float64) float64 { return net * 1.21 }\n",
		"README.md":      "# indexed\n",
	}, "first")

	pool := graphPool(t)
	idx := &indexing.Indexer{
		Git:        gitClient,
		Graph:      graph.NewStore(pool),
		Vectors:    graph.NewVectorStore(pool),
		Embedder:   stubEmbedder{},
		HMACSecret: testHMACSecret,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := idx.HandlePush(ctx, events.PushEvent{
		OrgID: orgID, RepoID: repoID, Ref: "refs/heads/main", OldSHA: zeroSHA, NewSHA: first,
	}); err != nil {
		t.Fatalf("HandlePush (first push): %v", err)
	}

	scoped := scopedCtx(orgID)
	if paths := symbolPaths(t, pool, scoped, orgID); !paths["billing/vat.go"] {
		t.Fatalf("symbol paths after the first push = %v, want billing/vat.go", paths)
	}
	for _, p := range []string{"billing/vat.go", "README.md"} {
		if n := chunkCount(t, pool, scoped, orgID, p); n == 0 {
			t.Fatalf("no chunks stored for %s after the first push", p)
		}
	}

	second := commitFiles(t, work, map[string]string{
		"config/parse.go": "package config\n\nfunc Parse(b []byte) map[string]string { return nil }\n",
	}, "second")
	if err := idx.HandlePush(ctx, events.PushEvent{
		OrgID: orgID, RepoID: repoID, Ref: "refs/heads/main", OldSHA: first, NewSHA: second,
	}); err != nil {
		t.Fatalf("HandlePush (second push): %v", err)
	}
	if paths := symbolPaths(t, pool, scoped, orgID); !paths["config/parse.go"] || !paths["billing/vat.go"] {
		t.Fatalf("symbol paths after the second push = %v, want both files", paths)
	}
}

// TestBranchDeletionIsAcknowledgedNotRetried checks a push that deletes a
// ref: there is no commit to index, and treating it as a failure would leave
// the event pending and redelivered forever.
func TestBranchDeletionIsAcknowledgedNotRetried(t *testing.T) {
	gitClient, _, _ := realGitService(t)
	pool := graphPool(t)
	idx := &indexing.Indexer{
		Git:        gitClient,
		Graph:      graph.NewStore(pool),
		Vectors:    graph.NewVectorStore(pool),
		Embedder:   stubEmbedder{},
		HMACSecret: testHMACSecret,
	}
	err := idx.HandlePush(context.Background(), events.PushEvent{
		OrgID: uuid.New(), RepoID: uuid.New(), Ref: "refs/heads/gone",
		OldSHA: "1111111111111111111111111111111111111111", NewSHA: zeroSHA,
	})
	if err != nil {
		t.Fatalf("HandlePush (branch deletion) = %v, want nil so the event is acknowledged", err)
	}
}
