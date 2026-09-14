// Command engineering-graph runs the engineering-graph service: the code
// graph (symbols, dependencies, tests, history), persistent knowledge, the
// indexer that keeps both in sync with pushes, and per-Work-Item context
// assembly.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/ctxasm"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/indexing"
	"github.com/novaforge/novaforge/internal/knowledge"
	"github.com/novaforge/novaforge/internal/service"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/novaforge/novaforge/internal/work"
)

// defaultGRPCPort is used when GRPC_PORT is not set in the environment.
const defaultGRPCPort = 9097

func main() {
	cfg := service.LoadConfig()
	if cfg.DatabaseURL == "" {
		log.Fatal("engineering-graph: DATABASE_URL is required")
	}
	if cfg.RedisURL == "" {
		log.Fatal("engineering-graph: REDIS_URL is required")
	}
	if cfg.IdentityAddr == "" {
		log.Fatal("engineering-graph: IDENTITY_ADDR is required")
	}
	if cfg.GitAddr == "" {
		log.Fatal("engineering-graph: GIT_ADDR is required")
	}
	if cfg.GRPCPort == 0 {
		cfg.GRPCPort = defaultGRPCPort
	}

	if err := database.Migrate(cfg.DatabaseURL, "graph", graph.MigrationsFS); err != nil {
		log.Fatalf("engineering-graph: migrate graph schema: %v", err)
	}
	if err := database.Migrate(cfg.DatabaseURL, "knowledge", knowledge.MigrationsFS); err != nil {
		log.Fatalf("engineering-graph: migrate knowledge schema: %v", err)
	}

	ctx := context.Background()

	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("engineering-graph: connect database: %v", err)
	}
	defer pool.Close()

	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatalf("engineering-graph: parse redis url: %v", err)
	}
	rdb := redis.NewClient(redisOpts)
	defer rdb.Close()

	identityConn, err := grpc.NewClient(cfg.IdentityAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("engineering-graph: dial identity service: %v", err)
	}
	defer identityConn.Close()
	identityClient := identityv1.NewIdentityServiceClient(identityConn)

	gitConn, err := grpc.NewClient(cfg.GitAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("engineering-graph: dial git-platform: %v", err)
	}
	defer gitConn.Close()
	gitClient := gitv1.NewGitServiceClient(gitConn)

	graphStore := graph.NewStore(pool)
	vectors := graph.NewVectorStore(pool)
	knowledgeStore := knowledge.NewStore(pool)
	workStore := work.NewStore(pool)

	// Embedder is optional: EMBED_ENDPOINT/EMBED_MODEL are unset on a
	// deployment that has not configured a local embedding model yet.
	// SearchCode then falls back to a lexical match, SearchKnowledge
	// returns no results, and the indexer stores chunks without vectors —
	// a degrade, not a failure to start (see graph.GRPCServer's own doc).
	//
	// The indexer, however, cannot store anything without vectors (the
	// column is NOT NULL), so an unconfigured embedder means an empty code
	// index — which is why a configured one is probed here.
	var embedder graph.Embedder
	if endpointEmbedder, embedErr := graph.NewEndpointEmbedder(); embedErr != nil {
		log.Printf("engineering-graph: embedder not configured, code search is lexical over an index that stays empty: %v", embedErr)
	} else {
		embedder = endpointEmbedder
		// A model of the wrong width cannot store one chunk, and waiting does
		// not fix it: refuse to start rather than run a code index that
		// silently never fills. An unreachable gateway may come back, so that
		// is only reported.
		probeCtx, cancelProbe := context.WithTimeout(ctx, 30*time.Second)
		if err := graph.VerifyEmbedder(probeCtx, embedder); err != nil {
			if errors.Is(err, graph.ErrEmbeddingWidth) {
				log.Fatalf("engineering-graph: %v", err)
			}
			log.Printf("engineering-graph: embedding model did not answer the startup probe (indexing retries per push): %v", err)
		}
		cancelProbe()
	}

	assemble := newAssembleFunc(gitClient, graphStore, vectors, knowledgeStore, workStore)

	grpcServer := graph.NewGRPCServer(graphStore, vectors, knowledgeStore, workStore, embedder, assemble)

	// Callers are resolved the same way every service resolves them: a
	// person's credential through identity, or a platform service token
	// verified locally. Each service keeping its own copy of this is how two
	// services end up disagreeing about who is allowed in — and they did:
	// an agent run's service token was understood by git-platform and
	// refused here, so every work.get an agent made was denied.
	srv := grpc.NewServer(grpc.UnaryInterceptor(
		svcauth.UnaryServerInterceptor(identityClient, cfg.HMACSecret)))
	graphv1.RegisterGraphServiceServer(srv, grpcServer)

	idx := &indexing.Indexer{
		RDB:      rdb,
		Git:      gitClient,
		Graph:    graphStore,
		Vectors:  vectors,
		Embedder: embedder,
		// Without it the indexer reads repositories anonymously, and the git
		// service refuses every read: nothing was indexed until this was set.
		HMACSecret: cfg.HMACSecret,
	}
	indexerCtx, cancelIndexer := context.WithCancel(ctx)
	defer cancelIndexer()
	go func() {
		if err := idx.Run(indexerCtx); err != nil && indexerCtx.Err() == nil {
			log.Printf("engineering-graph: indexer stopped: %v", err)
		}
	}()

	check := func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("database: %w", err)
		}
		if err := rdb.Ping(ctx).Err(); err != nil {
			return fmt.Errorf("redis: %w", err)
		}
		return nil
	}

	if err := service.Serve(ctx, cfg, srv, check); err != nil {
		log.Fatalf("engineering-graph: serve: %v", err)
	}
}

// newAssembleFunc adapts ctxasm.Assemble into a graph.AssembleContextFunc:
// it resolves the named Work Item, runs the six-signal assembly, and
// converts the result into graph's gRPC-boundary shapes.
func newAssembleFunc(gitClient gitv1.GitServiceClient, graphStore *graph.Store, vectors *graph.VectorStore, knowledgeStore *knowledge.Store, workStore *work.Store) graph.AssembleContextFunc {
	return func(ctx context.Context, orgID, repoID uuid.UUID, workItemKey string, tokenBudget int) (graph.ContextBundle, error) {
		item, err := workStore.GetByKey(ctx, orgID, workItemKey)
		if err != nil {
			return graph.ContextBundle{}, fmt.Errorf("resolve work item %q: %w", workItemKey, err)
		}
		bundle, err := ctxasm.Assemble(ctx, ctxasm.Input{
			OrgID:       orgID,
			RepoID:      repoID,
			WorkItem:    item,
			TokenBudget: tokenBudget,
			Git:         gitClient,
			Graph:       graphStore,
			Vectors:     vectors,
			Knowledge:   knowledgeStore,
		})
		if err != nil {
			return graph.ContextBundle{}, err
		}
		return toGraphBundle(bundle), nil
	}
}

func toGraphBundle(b ctxasm.Bundle) graph.ContextBundle {
	files := make([]graph.ContextSnippet, len(b.Files))
	for i, f := range b.Files {
		files[i] = graph.ContextSnippet{Path: f.Path, StartLine: f.StartLine, EndLine: f.EndLine, Text: f.Text, Signal: f.Signal}
	}
	entries := make([]graph.KnowledgeSummary, len(b.Knowledge))
	for i, e := range b.Knowledge {
		entries[i] = graph.KnowledgeSummary{ID: e.ID.String(), Key: e.Key, Kind: e.Kind, Title: e.Title, Body: e.Body, CreatedAt: e.CreatedAt}
	}
	return graph.ContextBundle{Files: files, Knowledge: entries, Tests: b.Tests, TokensEstimated: b.TokensEstimated}
}

// authInterceptor resolves the caller from the request's "authorization"
// metadata via the identity service. See cmd/git-platform/main.go, which
// this mirrors exactly.
func authInterceptor(identityClient identityv1.IdentityServiceClient) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if scope, ok := resolveScopeFromMetadata(ctx, identityClient); ok {
			ctx = authz.WithScope(ctx, scope)
		}
		return handler(ctx, req)
	}
}

func resolveScopeFromMetadata(ctx context.Context, identityClient identityv1.IdentityServiceClient) (authz.Scope, bool) {
	token := bearerTokenFromContext(ctx)
	if token == "" {
		return authz.Scope{}, false
	}
	org := metadataValue(ctx, "x-novaforge-org")
	subject, err := resolveSubject(ctx, identityClient, token, org)
	if err != nil {
		return authz.Scope{}, false
	}
	return subjectToScope(subject), true
}

func resolveSubject(ctx context.Context, identityClient identityv1.IdentityServiceClient, token, org string) (*identityv1.Subject, error) {
	if resp, err := identityClient.ResolveToken(ctx, &identityv1.ResolveTokenRequest{Token: token, Org: org}); err == nil {
		return resp.GetSubject(), nil
	}
	resp, err := identityClient.ResolveSession(ctx, &identityv1.ResolveSessionRequest{Token: token, Org: org})
	if err != nil {
		return nil, err
	}
	return resp.GetSubject(), nil
}

func metadataValue(ctx context.Context, key string) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if v := md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

func subjectToScope(subject *identityv1.Subject) authz.Scope {
	var scope authz.Scope
	scope.ActorID = parseUUIDOrNil(subject.GetUserId())
	scope.OrgID = parseUUIDOrNil(subject.GetOrgId())
	scope.ActorKind = subject.GetActorKind()
	if scope.ActorKind == "" {
		scope.ActorKind = "user"
	}
	return scope
}

func parseUUIDOrNil(raw string) uuid.UUID {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func bearerTokenFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return ""
	}
	return strings.TrimPrefix(values[0], "Bearer ")
}
