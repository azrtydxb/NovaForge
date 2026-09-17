package maintenance_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/indexing"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// Index real committed source, then expose the owning service over its actual
// authenticated RPC. No caller seeds expected graph findings into a fake.
func graphHistory(t *testing.T, git gitv1.GitServiceClient, org, repo uuid.UUID, branch string) graphv1.GraphServiceClient {
	t.Helper()
	url := proposeDBURL(t)
	if err := database.Migrate(url, "graph", graph.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := graph.NewStore(pool)
	t.Cleanup(func() { _ = store.PurgeOrganization(proposeScopedCtx(org)) })
	tok, err := svcauth.Mint(sweepSecret, "graph-fixture", org, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+tok)
	head, err := git.ListCommits(ctx, &gitv1.ListCommitsRequest{Repo: repo.String(), Ref: branch, Limit: 1})
	if err != nil || len(head.GetCommits()) != 1 {
		t.Fatalf("read fixture head: %v", err)
	}
	idx := &indexing.Indexer{Git: git, Graph: store, Vectors: graph.NewVectorStore(pool), Embedder: graphFixtureEmbedder{}, HMACSecret: sweepSecret}
	if err := idx.HandlePush(context.Background(), events.PushEvent{OrgID: org, RepoID: repo, Ref: "refs/heads/" + branch, NewSHA: head.GetCommits()[0].GetSha()}); err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(svcauth.UnaryServerInterceptor(nil, sweepSecret)))
	graphv1.RegisterGraphServiceServer(srv, graph.NewGRPCServer(store, nil, nil, nil, nil, nil))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithUnaryInterceptor(svcauth.ForwardIncomingCredential))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return graphv1.NewGraphServiceClient(conn)
}

// Only external embedding inference is substituted: these tests concern the
// real parser, graph transactions and authenticated evidence, not similarity.
type graphFixtureEmbedder struct{}

func (graphFixtureEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, 1024)
		out[i][0] = 1
	}
	return out, nil
}
