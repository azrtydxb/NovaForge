// Command work-reviews runs the work-reviews service: typed engineering
// intent (Work Items) and Engineering Runs (pull requests that carry plan
// and proof) over gRPC.
package main

import (
	"context"
	"fmt"
	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/swarm"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/reviews"
	"github.com/novaforge/novaforge/internal/service"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/novaforge/novaforge/internal/work"
)

// defaultGRPCPort is used when GRPC_PORT is not set in the environment.
const defaultGRPCPort = 9093

func main() {
	cfg := service.LoadConfig()
	if cfg.DatabaseURL == "" {
		log.Fatal("work-reviews: DATABASE_URL is required")
	}
	if cfg.IdentityAddr == "" {
		log.Fatal("work-reviews: IDENTITY_ADDR is required")
	}
	if cfg.GRPCPort == 0 {
		cfg.GRPCPort = defaultGRPCPort
	}

	if err := database.Migrate(cfg.DatabaseURL, "work", work.MigrationsFS); err != nil {
		log.Fatalf("work-reviews: migrate work schema: %v", err)
	}
	if err := database.Migrate(cfg.DatabaseURL, "reviews", reviews.MigrationsFS); err != nil {
		log.Fatalf("work-reviews: migrate reviews schema: %v", err)
	}

	ctx := context.Background()

	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("work-reviews: connect database: %v", err)
	}
	defer pool.Close()

	identityConn, err := grpc.NewClient(cfg.IdentityAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("work-reviews: dial identity service: %v", err)
	}
	defer identityConn.Close()
	identityClient := identityv1.NewIdentityServiceClient(identityConn)

	workStore := work.NewStore(pool)
	reviewsStore := reviews.NewStore(pool)

	workServer := work.NewGRPCServer(workStore)

	// Decomposition needs a model. Where none is configured the RPC reports
	// that plainly rather than returning an epic with no subtasks, which would
	// read as "this epic did not need breaking down".
	if cfg.AIEndpoint != "" && cfg.AIModel != "" {
		providerOptions, poErr := agentrun.ParseProviderOptions(cfg.AIProviderOptions)
		if poErr != nil {
			log.Fatalf("work-reviews: %v", poErr)
		}
		model, err := agentrun.NewModelClient(agentrun.ModelConfig{
			Endpoint: cfg.AIEndpoint, Model: cfg.AIModel, APIKey: cfg.AIAPIKey,
		})
		if err != nil {
			log.Printf("work-reviews: no decomposition (%v)", err)
		} else {
			planner := swarm.NewPlanner(model, workStore, swarm.DefaultAgentRoles)
			planner.ProviderOptions = providerOptions
			workServer.SetDecomposer(swarm.PlannerDecomposer{Planner: planner})
			log.Printf("work-reviews: decomposition enabled via %s", cfg.AIEndpoint)
		}
	} else {
		log.Println("work-reviews: no AI endpoint configured, decomposition is unavailable")
	}
	reviewsServer := reviews.NewGRPCServer(reviewsStore)

	// Auto-merge is considered after every review submission, and only ever
	// merges through the same gate check a person's merge passes.
	if cfg.GitAddr != "" && cfg.GatesAddr != "" {
		// A merge is asked for by a person, and the gate check and the git merge
		// it leads to are made on their behalf: their credential is forwarded.
		// Without it the gate controller received an anonymous call, refused
		// it with "no authorization scope", and every merge on the platform
		// was blocked — failing closed, but for a reason that had nothing to
		// do with the run's gates. Background callers (the maintenance sweep)
		// have no incoming credential and attach a service token themselves.
		gitConn, err := grpc.NewClient(cfg.GitAddr, grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithChainUnaryInterceptor(svcauth.ForwardIncomingCredential))
		if err != nil {
			log.Fatalf("work-reviews: dial git-platform: %v", err)
		}
		defer gitConn.Close()
		gatesConn, err := grpc.NewClient(cfg.GatesAddr, grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithChainUnaryInterceptor(svcauth.ForwardIncomingCredential))
		if err != nil {
			log.Fatalf("work-reviews: dial gates: %v", err)
		}
		defer gatesConn.Close()

		gitClient := gitv1.NewGitServiceClient(gitConn)

		// The maintenance scanners sweep every repository on an interval,
		// proposing Work Items for what they find. Nothing here executes a
		// fix or starts an agent: a proposal is a plain, unassigned Work
		// Item somebody decides about.
		if hours := cfg.MaintenanceIntervalHours; hours > 0 {
			go runMaintenanceScanners(ctx, workStore, gitClient, cfg.HMACSecret, time.Duration(hours)*time.Hour)
			log.Printf("work-reviews: maintenance scanners sweeping every %dh", hours)
		} else {
			log.Println("work-reviews: MAINTENANCE_INTERVAL_HOURS is unset; no maintenance sweep runs")
		}

		workServer.SetScanner(newRepositoryScanner(workStore, gitClient))

		// The same Merger backs the MergeRun RPC and auto-merge, so a person
		// merging and the platform merging pass the identical gate check.
		reviewsServer.Merger = &reviews.Merger{
			Store: reviewsStore,
			Gates: reviews.GatesClient{Gates: gatesv1.NewGatesServiceClient(gatesConn)},
			Git:   gitClient,
		}

		reviewsServer.AutoMerge = newAutoMerger(
			reviews.AutoMergePolicy{
				Enabled:         cfg.AutoMergeEnabled,
				MaxFilesChanged: cfg.AutoMergeMaxFiles,
			},
			reviewsStore,
			gitClient,
			gatesv1.NewGatesServiceClient(gatesConn),
			workStore,
		)
		if reviewsServer.AutoMerge != nil {
			log.Printf("work-reviews: auto-merge enabled, cap %d files", cfg.AutoMergeMaxFiles)
		} else {
			log.Println("work-reviews: auto-merge is disabled by policy")
		}
	}

	// A decomposed epic is only worth decomposing if something then starts
	// its ready subtasks. AGENTS_ADDR unset means this deployment runs no
	// agents, which is a legitimate configuration — it is said out loud
	// rather than left as silence.
	if cfg.AgentsAddr != "" {
		agentsConn, err := grpc.NewClient(cfg.AgentsAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Fatalf("work-reviews: dial agent-runtime: %v", err)
		}
		defer agentsConn.Close()
		go runSwarmScheduler(ctx, workStore, agentsv1.NewAgentServiceClient(agentsConn), cfg.HMACSecret)
		log.Printf("work-reviews: swarm scheduler ticking every %s via %s", swarmTickInterval, cfg.AgentsAddr)
	} else {
		log.Println("work-reviews: AGENTS_ADDR is unset; ready subtasks will not be started automatically")
	}

	// Callers are resolved the same way every service resolves them: a
	// person's credential through identity, or a platform service token
	// verified locally. Each service keeping its own copy of this is how two
	// services end up disagreeing about who is allowed in — and they did:
	// an agent run's service token was understood by git-platform and
	// refused here, so every work.get an agent made was denied.
	srv := grpc.NewServer(grpc.UnaryInterceptor(
		svcauth.UnaryServerInterceptor(identityClient, cfg.HMACSecret)))
	workv1.RegisterWorkServiceServer(srv, workServer)
	reviewsv1.RegisterReviewsServiceServer(srv, reviewsServer)

	check := func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("database: %w", err)
		}
		return nil
	}

	if err := service.Serve(ctx, cfg, srv, check); err != nil {
		log.Fatalf("work-reviews: serve: %v", err)
	}
}

// authInterceptor resolves the caller from the request's "authorization"
// metadata via the identity service (a session or personal access token)
// and attaches the resulting authz.Scope to the request context, so every
// RPC derives its organization from context rather than from the request
// message. See cmd/git-platform/main.go, which this mirrors exactly: a
// credential says who the caller is, not which organization they act in —
// the org travels separately as the "x-novaforge-org" metadata key and
// identity verifies membership before granting a scope for it.
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
	scope.Role = subject.GetRole()
	if scope.ActorKind == "" {
		scope.ActorKind = "user"
	}
	return scope
}

// parseUUIDOrNil parses raw as a uuid.UUID, returning uuid.Nil for an empty
// or malformed value rather than an error: Subject.OrgId is legitimately
// empty for a credential with no organization selected, and authz.Scope's
// zero OrgID (uuid.Nil) already denies every organization-scoped request
// for it.
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
