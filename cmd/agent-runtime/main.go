// Command agent-runtime runs the agent-runtime service: agent identities,
// isolated per-run Kubernetes workspaces, the model/tool execution loop,
// and the event stream a run's state changes and tool calls are published
// to.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/service"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/novaforge/novaforge/internal/tools"
	"github.com/novaforge/novaforge/internal/workspace"
)

// defaultGRPCPort is used when GRPC_PORT is not set in the environment.
const defaultGRPCPort = 9095

// reapInterval is how often the workspace reaper runs.
const reapInterval = 5 * time.Minute

// reapOlderThan is how old a workspace must be before the reaper destroys
// it — a safety net for a run whose own completion path failed to clean up
// after itself, not the primary cleanup path.
const reapOlderThan = time.Hour

// defaultWorkspaceImage is the container image an agent run's workspace pod
// runs when the repository declares none of its own.
const defaultWorkspaceImage = "golang:1.26"

func main() {
	cfg := service.LoadConfig()
	if cfg.DatabaseURL == "" {
		log.Fatal("agent-runtime: DATABASE_URL is required")
	}
	if cfg.RedisURL == "" {
		log.Fatal("agent-runtime: REDIS_URL is required")
	}
	if cfg.IdentityAddr == "" {
		log.Fatal("agent-runtime: IDENTITY_ADDR is required")
	}
	if cfg.GitAddr == "" {
		log.Fatal("agent-runtime: GIT_ADDR is required")
	}
	if cfg.WorkAddr == "" {
		log.Fatal("agent-runtime: WORK_ADDR is required")
	}
	if cfg.GRPCPort == 0 {
		cfg.GRPCPort = defaultGRPCPort
	}

	if err := database.Migrate(cfg.DatabaseURL, "agents", agents.MigrationsFS); err != nil {
		log.Fatalf("agent-runtime: migrate agents schema: %v", err)
	}
	if err := database.Migrate(cfg.DatabaseURL, "gitplatform", capability.MigrationsFS); err != nil {
		log.Fatalf("agent-runtime: migrate gitplatform (capability) schema: %v", err)
	}

	ctx := context.Background()

	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("agent-runtime: connect database: %v", err)
	}
	defer pool.Close()

	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatalf("agent-runtime: parse redis url: %v", err)
	}
	rdb := redis.NewClient(redisOpts)
	defer rdb.Close()

	identityConn, err := grpc.NewClient(cfg.IdentityAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("agent-runtime: dial identity service: %v", err)
	}
	defer identityConn.Close()
	identityClient := identityv1.NewIdentityServiceClient(identityConn)

	gitConn, err := grpc.NewClient(cfg.GitAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(svcauth.ForwardIncomingCredential))
	if err != nil {
		log.Fatalf("agent-runtime: dial git-platform: %v", err)
	}
	defer gitConn.Close()
	gitClient := gitv1.NewGitServiceClient(gitConn)

	// Calls this service makes on a caller's behalf carry that caller's
	// credential. Without it agent-runtime authenticated a request and then
	// called work-reviews anonymously, which refused — the same defect the
	// edge had, one hop further in.
	workConn, err := grpc.NewClient(cfg.WorkAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(svcauth.ForwardIncomingCredential))
	if err != nil {
		log.Fatalf("agent-runtime: dial work-reviews: %v", err)
	}
	defer workConn.Close()
	workClient := workv1.NewWorkServiceClient(workConn)

	// The graph service answers the three code-intelligence tools — search,
	// symbol lookup, dependency lookup — that git-platform has no RPC for.
	// Without it those tools report themselves unavailable rather than
	// returning a plausible empty result.
	var graphClient graphv1.GraphServiceClient
	if cfg.GraphAddr != "" {
		graphConn, err := grpc.NewClient(cfg.GraphAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithChainUnaryInterceptor(svcauth.ForwardIncomingCredential))
		if err != nil {
			log.Fatalf("agent-runtime: dial engineering-graph: %v", err)
		}
		defer graphConn.Close()
		graphClient = graphv1.NewGraphServiceClient(graphConn)
	} else {
		log.Println("agent-runtime: GRAPH_ADDR is unset; repo.search, repo.get_symbol and repo.get_dependencies will report themselves unavailable")
	}
	reviewsClient := reviewsv1.NewReviewsServiceClient(workConn)
	_ = reviewsClient // reserved for the reviews-backed gate.status tool once ReviewsService grows a GateStatus RPC.

	store := agents.NewStore(pool)
	grants := capability.NewStore(pool)
	audit := agents.NewAuditLog(pool)

	var provisioner *workspace.Provisioner
	if k8sConfig, err := rest.InClusterConfig(); err == nil {
		clientset, err := kubernetes.NewForConfig(k8sConfig)
		if err != nil {
			log.Fatalf("agent-runtime: build kubernetes client: %v", err)
		}
		provisioner = workspace.NewProvisioner(clientset)
	} else {
		// Not running in a cluster: workspace provisioning and the reaper
		// are unavailable, exactly like graph.Assemble degrades when its
		// dependency is unset. This lets the gRPC surface (CreateAgent,
		// ListAgents, GetRun, CancelRun, StreamRunEvents) keep working in
		// an environment with no Kubernetes API to reach.
		log.Printf("agent-runtime: no in-cluster Kubernetes config available (%v); workspace provisioning and the reaper are disabled", err)
	}

	execute := newExecuteFunc(store, grants, audit, provisioner, gitClient, graphClient, workClient, cfg)
	grpcServer := agents.NewGRPCServer(store, grants, rdb, workClient, execute)

	// Callers are resolved the same way every other service resolves them:
	// a person's credential through identity, or a platform service token
	// verified locally. The swarm scheduler in work-reviews starts runs with
	// the latter, so an interceptor that only understood the former would
	// silently refuse every autonomously started run.
	srv := grpc.NewServer(grpc.UnaryInterceptor(
		svcauth.UnaryServerInterceptor(identityClient, cfg.HMACSecret)))
	agentsv1.RegisterAgentServiceServer(srv, grpcServer)

	if provisioner != nil {
		go runReaper(ctx, provisioner)
	}

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
		log.Fatalf("agent-runtime: serve: %v", err)
	}
}

// runReaper destroys any workspace older than reapOlderThan every
// reapInterval, until ctx is cancelled.
func runReaper(ctx context.Context, provisioner *workspace.Provisioner) {
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := provisioner.Reap(ctx, reapOlderThan)
			if err != nil {
				log.Printf("agent-runtime: workspace reap failed: %v", err)
				continue
			}
			if n > 0 {
				log.Printf("agent-runtime: reaped %d stale workspace(s)", n)
			}
		}
	}
}

// newExecuteFunc builds the agents.ExecuteFunc that drives a run once
// StartRun has moved it to "running": it provisions an isolated workspace,
// runs the model/tool loop, persists the resulting terminal state, and
// tears the workspace down. When provisioner is nil (no Kubernetes API
// reachable), it returns nil so StartRun's degrade path applies instead.
func newExecuteFunc(store *agents.Store, grants *capability.Store, audit *agents.AuditLog, provisioner *workspace.Provisioner, gitClient gitv1.GitServiceClient, graphClient graphv1.GraphServiceClient, workClient workv1.WorkServiceClient, cfg service.Config) agents.ExecuteFunc {
	if provisioner == nil {
		return nil
	}
	return func(ctx context.Context, run agents.Run) {
		// An agent run outlives the request that started it, so it cannot
		// borrow that caller's credential: by the time a tool calls
		// git-platform the request is long gone. The run presents a service
		// token naming only its own organization, which is how the CI
		// scheduler reaches git-platform for the same reason. Without it
		// every tool call reaches its service anonymously and is refused —
		// an agent that can call nothing looks exactly like an agent that
		// chose to do nothing.
		ctx, err := withRunIdentity(ctx, cfg.HMACSecret, run.OrgID)
		if err != nil {
			log.Printf("agent-runtime: run %s: %v", run.ID, err)
			finishRun(ctx, store, run, "failed")
			return
		}

		ws, err := provisioner.Create(ctx, run.ID, workspace.Spec{Image: defaultWorkspaceImage})
		if err != nil {
			log.Printf("agent-runtime: provision workspace for run %s: %v", run.ID, err)
			finishRun(ctx, store, run, "failed")
			return
		}
		defer func() {
			if err := provisioner.Destroy(context.Background(), run.ID); err != nil {
				log.Printf("agent-runtime: destroy workspace for run %s: %v", run.ID, err)
			}
		}()

		model, err := agentrun.NewModelClient(agentrun.ModelConfig{Endpoint: cfg.AIEndpoint, Model: cfg.AIModel, APIKey: cfg.AIAPIKey})
		if err != nil {
			log.Printf("agent-runtime: build model client for run %s: %v", run.ID, err)
			finishRun(ctx, store, run, "failed")
			return
		}

		budget := agents.NewBudget(run.WallclockLimit, run.TokenLimit, run.CostLimitMicros)

		var grant capability.Grant
		if run.GrantID != uuid.Nil {
			// The grant was already issued and validated by StartRun; the
			// tool registry needs its contents (in particular WriteBranch)
			// so git.commit's capability check enforces the same scoped
			// branch the grant was issued for.
			grant, err = grants.Resolve(ctx, run.GrantID)
			if err != nil {
				log.Printf("agent-runtime: resolve grant %s for run %s: %v", run.GrantID, run.ID, err)
				finishRun(ctx, store, run, "failed")
				return
			}
		}

		reg := tools.NewRegistry(tools.Runtime{
			RunID:         run.ID,
			Grant:         grant,
			Budget:        budget,
			WorkspaceRoot: "/workspace/" + ws.PodName,
			Git:           newGitAdapter(gitClient, graphClient),
			Work:          newWorkAdapter(workClient),
		}, audit)

		loop := agentrun.NewLoop(model, budget, audit)
		providerOptions, poErr := agentrun.ParseProviderOptions(cfg.AIProviderOptions)
		if poErr != nil {
			log.Printf("agent-runtime: %v", poErr)
			finishRun(ctx, store, run, "failed")
			return
		}
		loop.ProviderOptions = providerOptions
		result, err := loop.Execute(ctx, run, reg)
		state := "failed"
		if err == nil {
			state = result.State
		} else {
			log.Printf("agent-runtime: run %s loop failed: %v", run.ID, err)
		}
		finishRun(ctx, store, run, state)
	}
}

// finishRun transitions run to its terminal state and publishes that
// change, best-effort: a failure here is logged, since the run's own
// outcome (already computed) must not be lost even if the state write or
// the event publish fails.
func finishRun(ctx context.Context, store *agents.Store, run agents.Run, state string) {
	scoped := authz.WithScope(ctx, authz.Scope{OrgID: run.OrgID, ActorID: run.AgentID, ActorKind: "agent"})
	if err := store.SetRunState(scoped, run.ID, state); err != nil {
		log.Printf("agent-runtime: set run %s state to %s: %v", run.ID, state, err)
	}
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

// withRunIdentity attaches a service token for orgID to every outbound call
// an agent run makes. The token names one organization, so a run cannot
// reach outside the organization it belongs to even if a tool were asked to.
func withRunIdentity(ctx context.Context, hmacSecret string, orgID uuid.UUID) (context.Context, error) {
	tok, err := svcauth.Mint(hmacSecret, "agent-run", orgID, svcauth.DefaultTTL)
	if err != nil {
		return ctx, fmt.Errorf("mint service token: %w", err)
	}
	return metadata.AppendToOutgoingContext(ctx,
		"authorization", "Bearer "+tok,
		"x-novaforge-org", orgID.String(),
	), nil
}
