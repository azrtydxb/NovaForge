// Command agent-runtime runs the agent-runtime service: agent identities,
// isolated per-run Kubernetes workspaces, the model/tool execution loop,
// and the event stream a run's state changes and tool calls are published
// to.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/cleanup"
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
	var ciClient civ1.CIServiceClient
	if cfg.CIAddr != "" {
		ciConn, err := grpc.NewClient(cfg.CIAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithChainUnaryInterceptor(svcauth.ForwardIncomingCredential))
		if err != nil {
			log.Fatalf("agent-runtime: dial ci-runner: %v", err)
		}
		defer ciConn.Close()
		ciClient = civ1.NewCIServiceClient(ciConn)
	} else {
		log.Println("agent-runtime: CI_ADDR is unset; ci.* tools will report themselves unavailable")
	}

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

	// mcp-server keeps the organization's register of approved external MCP
	// servers. The run's own credential is already on its context, so this
	// connection forwards nothing of its own.
	var mcpClient mcpv1.McpServiceClient
	if cfg.MCPAddr != "" {
		mcpConn, err := grpc.NewClient(cfg.MCPAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Fatalf("agent-runtime: dial mcp-server: %v", err)
		}
		defer mcpConn.Close()
		mcpClient = mcpv1.NewMcpServiceClient(mcpConn)
	} else {
		log.Println("agent-runtime: MCP_ADDR is unset; no external MCP server is offered to agents")
	}

	store := agents.NewStore(pool)
	// A deleted repository's Agent Runs, and a deleted organization's runs and
	// agents, are cancelled and removed when the deletion is announced.
	cleanup.AgentRuntime(store, cleanup.RedisRunsPublisher(rdb)).Run(ctx, rdb, "agent-runtime")
	grants := capability.NewStore(pool)
	audit := agents.NewAuditLog(pool)

	var provisioner *workspace.Provisioner
	if k8sConfig, err := rest.InClusterConfig(); err == nil {
		clientset, err := kubernetes.NewForConfig(k8sConfig)
		if err != nil {
			log.Fatalf("agent-runtime: build kubernetes client: %v", err)
		}
		provisioner = workspace.NewProvisioner(clientset).WithRESTConfig(k8sConfig)
	} else {
		// Not running in a cluster: workspace provisioning and the reaper
		// are unavailable, exactly like graph.Assemble degrades when its
		// dependency is unset. This lets the gRPC surface (CreateAgent,
		// ListAgents, GetRun, CancelRun, StreamRunEvents) keep working in
		// an environment with no Kubernetes API to reach.
		log.Printf("agent-runtime: no in-cluster Kubernetes config available (%v); workspace provisioning and the reaper are disabled", err)
	}

	// A run's cost limit is enforced against the price of the model it runs
	// on. A malformed price list stops the service: read as "no prices" it
	// would silently refuse every cost limit, and a typo in one model's entry
	// must not look like a deployment that chose not to bound cost.
	prices, err := agents.ParseModelPrices(cfg.AIModelPrices)
	if err != nil {
		log.Fatalf("agent-runtime: %v", err)
	}
	var price *agents.TokenPrice
	if p, ok := prices[cfg.AIModel]; ok {
		price = &p
	} else {
		log.Printf("agent-runtime: AI_MODEL_PRICES has no price for %q; runs are bounded by wall clock and tokens, and a cost limit is refused", cfg.AIModel)
	}

	execute := newExecuteFunc(store, grants, audit, rdb, price, provisioner, gitClient, graphClient, workClient, reviewsClient, ciClient, mcpClient, cfg)
	grpcServer := agents.NewGRPCServer(store, grants, rdb, workClient, execute)
	grpcServer.Audit = audit
	grpcServer.Price = price

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
	go runOrphanRecovery(ctx, store, rdb)

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

// workspaceReadyTimeout bounds how long a run waits for its workspace pod,
// including a first pull of the workspace image onto a node.
const workspaceReadyTimeout = 5 * time.Minute

// orphanRecoveryInterval is how often runs left "running" by a replica that
// died are looked for.
const orphanRecoveryInterval = time.Minute

// runOrphanRecovery settles runs whose executing replica is gone, so their
// branch locks are released (agents.RecoverOrphanedRuns). It runs once at
// start — a restart is exactly when this replica's own runs were orphaned —
// and then on an interval, whether or not this replica can provision
// workspaces: an orphan holds a branch however it was started.
func runOrphanRecovery(ctx context.Context, store *agents.Store, rdb *redis.Client) {
	ticker := time.NewTicker(orphanRecoveryInterval)
	defer ticker.Stop()
	for {
		n, err := agents.RecoverOrphanedRuns(ctx, store, rdb, agents.OrphanGrace)
		if err != nil {
			log.Printf("agent-runtime: recover orphaned runs: %v", err)
		}
		if n > 0 {
			log.Printf("agent-runtime: settled %d orphaned run(s) and released their branches", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runReaper destroys expired workspaces older than reapOlderThan every
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
func newExecuteFunc(store *agents.Store, grants *capability.Store, audit *agents.AuditLog, rdb *redis.Client, price *agents.TokenPrice, provisioner *workspace.Provisioner, gitClient gitv1.GitServiceClient, graphClient graphv1.GraphServiceClient, workClient workv1.WorkServiceClient, reviewsClient reviewsv1.ReviewsServiceClient, ciClient civ1.CIServiceClient, mcpClient mcpv1.McpServiceClient, cfg service.Config) agents.ExecuteFunc {
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
		ctx, err := agentrun.WithRunIdentity(ctx, cfg.HMACSecret, run.OrgID, run.AgentID, agentrun.RunCredentialTTL(run))
		if err != nil {
			log.Printf("agent-runtime: run %s: %v", run.ID, err)
			finishRun(ctx, store, rdb, run, "failed", fmt.Sprintf("could not give the run an identity: %v", err))
			return
		}

		// The per-run namespace carries the run's isolation — its own
		// network policy and resource quota — and is torn down with the run.
		if _, err := provisioner.Create(ctx, run.ID, workspace.Spec{
			Image: workspace.DefaultImage, CPULimit: "2", MemLimit: "4Gi",
			// The workspace must live at least as long as the run credential,
			// including the margin reserved for settling evidence on timeout.
			ExpiresAt: time.Now().Add(agentrun.RunCredentialTTL(run)),
		}); err != nil {
			log.Printf("agent-runtime: provision workspace for run %s: %v", run.ID, err)
			finishRun(ctx, store, rdb, run, "failed", fmt.Sprintf("could not provision the run workspace: %v", err))
			return
		}
		defer func() {
			if err := provisioner.Destroy(context.Background(), run.ID); err != nil {
				log.Printf("agent-runtime: destroy workspace for run %s: %v", run.ID, err)
			}
		}()
		// The workspace pod is where the run's files are staged and its
		// commands run, so the run cannot start until the pod has.
		if err := provisioner.WaitReady(ctx, run.ID, workspaceReadyTimeout); err != nil {
			log.Printf("agent-runtime: workspace for run %s: %v", run.ID, err)
			finishRun(ctx, store, rdb, run, "failed", fmt.Sprintf("the run workspace never became ready: %v", err))
			return
		}
		ref, err := seedWorkspace(ctx, gitClient, provisioner, run.ID, run.RepoID)
		if err != nil {
			log.Printf("agent-runtime: seed workspace for run %s: %v", run.ID, err)
			finishRun(ctx, store, rdb, run, "failed", fmt.Sprintf("could not copy the repository into the workspace: %v", err))
			return
		}
		log.Printf("agent-runtime: run %s workspace holds %s", run.ID, ref)

		var grant capability.Grant
		if run.GrantID != uuid.Nil {
			// The grant was already issued and validated by StartRun; the
			// tool registry needs its contents (in particular WriteBranch)
			// so git.commit's capability check enforces the same scoped
			// branch the grant was issued for.
			grant, err = grants.Resolve(ctx, run.GrantID)
			if err != nil {
				log.Printf("agent-runtime: resolve grant %s for run %s: %v", run.GrantID, run.ID, err)
				finishRun(ctx, store, rdb, run, "failed", fmt.Sprintf("could not resolve the run capability grant: %v", err))
				return
			}
		}

		providerOptions, poErr := agentrun.ParseProviderOptions(cfg.AIProviderOptions)
		if poErr != nil {
			log.Printf("agent-runtime: %v", poErr)
			finishRun(ctx, store, rdb, run, "failed", poErr.Error())
			return
		}

		// The runner reads the repository's .novaforge configuration and the
		// context assembled for the Work Item before the first model turn;
		// both used to reach no run at all.
		runner := &agentrun.Runner{
			Git:   gitClient,
			Work:  workClient,
			Graph: graphClient,
			Runs:  store,
			Audit: audit,
			NewModel: func(model string) (provider.LanguageModel, error) {
				// A repository may pin a model for its agents; otherwise the
				// deployment's model runs.
				if model == "" {
					model = cfg.AIModel
				}
				return agentrun.NewModelClient(agentrun.ModelConfig{Endpoint: cfg.AIEndpoint, Model: model, APIKey: cfg.AIAPIKey})
			},
			ProviderOptions: providerOptions,
			Price:           price,
			MCP:             mcpClient,
		}
		result := runner.Run(ctx, run, tools.Runtime{
			Grant:     grant,
			Workspace: &podWorkspace{provisioner: provisioner, runID: run.ID},
			Git:       newGitAdapter(gitClient, graphClient),
			Work:      tools.NewWorkClient(workClient),
			Graph:     newGraphAdapter(graphClient, run.RepoID.String()),
			Reviews:   newReviewsAdapter(reviewsClient),
			CI:        newCIAdapter(ciClient),
			Knowledge: tools.NewKnowledgeClient(graphClient, run.RepoID.String(), run.ID.String()),
		})
		durableState, err := finishResult(ctx, store, rdb, run, result)
		if err != nil {
			log.Printf("agent-runtime: run %s accounting unresolved: %v", run.ID, err)
			return
		}
		if durableState == "succeeded" {
			openEngineeringRun(ctx, store, workClient, reviewsClient, gitClient, run, cfg)
		}
	}
}

// openEngineeringRun records which agent and model produced a succeeded run
// and opens the Engineering Run its change is reviewed and merged through.
// Failing to do either does not unmake the run's success, so it is logged.
func openEngineeringRun(ctx context.Context, store *agents.Store, work workv1.WorkServiceClient, reviews reviewsv1.ReviewsServiceClient, git gitv1.GitServiceClient, run agents.Run, cfg service.Config) {
	// The run's own token was minted when it started and may have expired
	// over a long run; this is new work on its behalf, with a fresh one.
	callCtx, err := agentrun.WithRunIdentity(context.WithoutCancel(ctx), cfg.HMACSecret, run.OrgID, run.AgentID, agentrun.RunCredentialTTL(run))
	if err != nil {
		log.Printf("agent-runtime: run %s: %v", run.ID, err)
		return
	}
	scoped := authz.WithScope(callCtx, authz.Scope{OrgID: run.OrgID, ActorID: run.AgentID, ActorKind: "agent"})
	agent, err := store.GetAgent(scoped, run.AgentID)
	if err != nil {
		log.Printf("agent-runtime: run %s: resolve agent: %v", run.ID, err)
		return
	}
	item, err := work.GetItem(callCtx, &workv1.GetItemRequest{Id: run.WorkItemID.String()})
	if err != nil {
		log.Printf("agent-runtime: run %s: read work item: %v", run.ID, err)
		return
	}
	if err := store.RecordProvenance(scoped, run.ID, agents.Provenance{
		AgentName: agent.Name, ModelName: cfg.AIModel, WorkItemKey: item.GetItem().GetKey(),
		RunRef: run.Branch, SponsorName: run.SponsorID.String(),
	}); err != nil {
		log.Printf("agent-runtime: run %s: record provenance: %v", run.ID, err)
	}
	opened, err := agentrun.OpenEngineeringRun(callCtx, reviews, git, agentrun.EngineeringRunSpec{
		RepoID: run.RepoID, WorkItemID: run.WorkItemID, WorkItemKey: item.GetItem().GetKey(),
		Goal: item.GetItem().GetGoal(), Acceptance: item.GetItem().GetAcceptance(),
		Branch: run.Branch, AgentID: run.AgentID, AgentName: agent.Name, ModelName: cfg.AIModel,
	})
	switch {
	case err != nil:
		log.Printf("agent-runtime: run %s: open engineering run: %v", run.ID, err)
	case opened == nil:
		log.Printf("agent-runtime: run %s succeeded with no change on %s; no engineering run opened", run.ID, run.Branch)
	default:
		log.Printf("agent-runtime: run %s opened engineering run #%d", run.ID, opened.GetNumber())
	}
}

// finishRun settles run in its terminal state, best-effort: a failure is
// logged, since the run's own outcome (already computed) must not be lost to
// a failed state write or event publish.
//
// reason, when given, is recorded as why the run ended: a run that failed
// before its loop started has no audit summary, and "failed" alone tells the
// person reading it nothing.
func finishRun(ctx context.Context, store *agents.Store, rdb *redis.Client, run agents.Run, state, reason string) {
	// A run cancelled while its workspace was still being provisioned fails
	// that step with "context canceled" and arrives here as "failed"; it was
	// cancelled, and the row already says so.
	if errors.Is(context.Cause(ctx), agents.ErrOverBudget) {
		state = "over_budget"
	} else if errors.Is(ctx.Err(), context.Canceled) {
		state = "cancelled"
	}
	result := agentrun.Result{State: state, Summary: reason, Completion: agents.Completion{State: state, Summary: reason, Spend: agents.Spend{Reason: reason}}}
	if _, err := finishResult(ctx, store, rdb, run, result); err != nil {
		log.Printf("agent-runtime: run %s accounting unresolved: %v", run.ID, err)
	}
}

// finishResult retries the identical retained receipt, including an ambiguous
// commit response. Failure leaves accounting unresolved; never settle success
// with default counters. Crash recovery labels missing receipts unavailable.
func finishResult(ctx context.Context, store *agents.Store, rdb *redis.Client, run agents.Run, result agentrun.Result) (string, error) {
	scoped := authz.WithScope(context.WithoutCancel(ctx), authz.Scope{OrgID: run.OrgID, ActorID: run.AgentID, ActorKind: "agent"})
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		writeCtx, cancel := context.WithTimeout(scoped, 10*time.Second)
		var state string
		state, err = store.CompleteRun(writeCtx, run.ID, result.Completion)
		cancel()
		if err == nil {
			if cleanupErr := agents.SettleRun(scoped, store, rdb, run, state); cleanupErr != nil {
				log.Printf("agent-runtime: cleanup for run %s: %v", run.ID, cleanupErr)
			}
			return state, nil
		}
	}
	return "", err
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
	scope.Role = subject.GetRole()
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
