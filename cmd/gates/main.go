// Command gates runs the gates service: the sole authority on merge
// eligibility (gate evaluation and MayMerge), human approvals, and
// short-lived secret leases.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/analysis"
	"github.com/novaforge/novaforge/internal/approvals"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gates"
	"github.com/novaforge/novaforge/internal/secrets"
	"github.com/novaforge/novaforge/internal/service"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// defaultGRPCPort is used when GRPC_PORT is not set in the environment.
const defaultGRPCPort = 9096

func main() {
	cfg := service.LoadConfig()
	if cfg.DatabaseURL == "" {
		log.Fatal("gates: DATABASE_URL is required")
	}
	if cfg.IdentityAddr == "" {
		log.Fatal("gates: IDENTITY_ADDR is required")
	}
	if cfg.GitAddr == "" {
		log.Fatal("gates: GIT_ADDR is required")
	}
	if cfg.WorkAddr == "" {
		log.Fatal("gates: WORK_ADDR is required")
	}
	if cfg.SecretsKEK == "" {
		log.Fatal("gates: SECRETS_KEK is required")
	}
	if cfg.GRPCPort == 0 {
		cfg.GRPCPort = defaultGRPCPort
	}

	if err := database.Migrate(cfg.DatabaseURL, "gates", gates.MigrationsFS); err != nil {
		log.Fatalf("gates: migrate gates schema: %v", err)
	}
	if err := database.Migrate(cfg.DatabaseURL, "approvals", approvals.MigrationsFS); err != nil {
		log.Fatalf("gates: migrate approvals schema: %v", err)
	}
	if err := database.Migrate(cfg.DatabaseURL, "secrets", secrets.MigrationsFS); err != nil {
		log.Fatalf("gates: migrate secrets schema: %v", err)
	}
	// capability_grants lives in the gitplatform schema; IssueLease resolves
	// grants by id, so it needs the table to exist even on a fresh database
	// the identity service has not touched yet (see cmd/identity/main.go).
	if err := database.Migrate(cfg.DatabaseURL, "gitplatform", capability.MigrationsFS); err != nil {
		log.Fatalf("gates: migrate gitplatform (capability) schema: %v", err)
	}

	ctx := context.Background()

	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("gates: connect database: %v", err)
	}
	defer pool.Close()

	identityConn, err := grpc.NewClient(cfg.IdentityAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("gates: dial identity service: %v", err)
	}
	defer identityConn.Close()
	identityClient := identityv1.NewIdentityServiceClient(identityConn)

	// The caller's credential is forwarded on outbound calls. ProposeGateChange
	// writes a branch and opens a run as the person who asked, and git-platform
	// and reviews both refuse a call that arrives with no caller — correctly.
	gitConn, err := grpc.NewClient(cfg.GitAddr, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(svcauth.ForwardIncomingCredential))
	if err != nil {
		log.Fatalf("gates: dial git-platform: %v", err)
	}
	defer gitConn.Close()
	gitClient := gitv1.NewGitServiceClient(gitConn)

	workConn, err := grpc.NewClient(cfg.WorkAddr, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(svcauth.ForwardIncomingCredential))
	if err != nil {
		log.Fatalf("gates: dial work-reviews: %v", err)
	}
	defer workConn.Close()
	workClient := workv1.NewWorkServiceClient(workConn)
	reviewsClient := reviewsv1.NewReviewsServiceClient(workConn)

	gatesStore := gates.NewStore(pool)
	approvalsStore := approvals.NewStore(pool)
	secretsBroker := secrets.NewBroker(pool, []byte(cfg.SecretsKEK))
	grants := capability.NewStore(pool)

	controller := &gates.Controller{
		Store:      gatesStore,
		Git:        gitClient,
		Runs:       newRunLookup(reviewsClient, workClient, gitClient),
		BuildInput: newInputBuilder(gitClient),
	}

	grpcServer := gates.NewGRPCServer(controller, approvalsStore, secretsBroker, grants)
	grpcServer.Proposals = &gates.Proposer{Git: gitClient, Reviews: reviewsClient}

	// Callers are resolved the same way every service resolves them: a
	// person's credential through identity, or a platform service token
	// verified locally. Each service keeping its own copy of this is how two
	// services end up disagreeing about who is allowed in — and they did:
	// an agent run's service token was understood by git-platform and
	// refused here, so every work.get an agent made was denied.
	srv := grpc.NewServer(grpc.UnaryInterceptor(
		svcauth.UnaryServerInterceptor(identityClient, cfg.HMACSecret)))
	gatesv1.RegisterGatesServiceServer(srv, grpcServer)

	check := func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("database: %w", err)
		}
		return nil
	}

	if err := service.Serve(ctx, cfg, srv, check); err != nil {
		log.Fatalf("gates: serve: %v", err)
	}
}

// newRunLookup resolves a gates.RunHead by asking the reviews and work
// services, and the git-platform service for the target branch's current
// head SHA — the gates schema never reads their tables directly.
func newRunLookup(reviewsClient reviewsv1.ReviewsServiceClient, workClient workv1.WorkServiceClient, gitClient gitv1.GitServiceClient) gates.RunLookup {
	return func(ctx context.Context, runID uuid.UUID) (gates.RunHead, error) {
		runResp, err := reviewsClient.GetRun(ctx, &reviewsv1.GetRunRequest{Id: runID.String()})
		if err != nil {
			return gates.RunHead{}, fmt.Errorf("get run %s: %w", runID, err)
		}
		run := runResp.GetRun()

		orgID, err := uuid.Parse(run.GetOrgId())
		if err != nil {
			return gates.RunHead{}, fmt.Errorf("run %s has invalid org_id: %w", runID, err)
		}
		repoID, err := uuid.Parse(run.GetRepoId())
		if err != nil {
			return gates.RunHead{}, fmt.Errorf("run %s has invalid repo_id: %w", runID, err)
		}

		var requiredGates []string
		if run.GetWorkItemId() != "" {
			itemResp, err := workClient.GetItem(ctx, &workv1.GetItemRequest{Id: run.GetWorkItemId()})
			if err != nil {
				return gates.RunHead{}, fmt.Errorf("get work item %s: %w", run.GetWorkItemId(), err)
			}
			requiredGates = itemResp.GetItem().GetRequiredGates()
		}

		repoName, err := resolveRepoName(ctx, gitClient, repoID)
		if err != nil {
			return gates.RunHead{}, err
		}

		branch := strings.TrimPrefix(run.GetTargetRef(), "refs/heads/")
		branchesResp, err := gitClient.ListBranches(ctx, &gitv1.ListBranchesRequest{Repo: repoName})
		if err != nil {
			return gates.RunHead{}, fmt.Errorf("list branches for %s: %w", repoName, err)
		}
		var headSHA string
		for _, ref := range branchesResp.GetRefs() {
			if ref.GetName() == branch {
				headSHA = ref.GetSha()
				break
			}
		}
		if headSHA == "" {
			return gates.RunHead{}, fmt.Errorf("branch %q not found in repo %s", branch, repoName)
		}

		return gates.RunHead{
			OrgID:         orgID,
			RepoID:        repoID,
			TargetRef:     run.GetTargetRef(),
			HeadSHA:       headSHA,
			WorkItemGates: requiredGates,
		}, nil
	}
}

// resolveRepoName looks up a repository's name from its id — the git
// transport RPCs (GetTree, GetBlob, ListBranches) address a repository by
// name within the caller's organization, not by id.
func resolveRepoName(ctx context.Context, gitClient gitv1.GitServiceClient, repoID uuid.UUID) (string, error) {
	reposResp, err := gitClient.ListRepos(ctx, &gitv1.ListReposRequest{})
	if err != nil {
		return "", fmt.Errorf("list repos: %w", err)
	}
	for _, r := range reposResp.GetRepos() {
		if r.GetId() == repoID.String() {
			return r.GetName(), nil
		}
	}
	return "", fmt.Errorf("repo %s not found", repoID)
}

// newInputBuilder materializes the run's target commit into a fresh
// temporary workspace by walking GetTree/GetBlob recursively, so a gate
// runner running analysis tools sees a real checkout on disk. The
// workspace is removed once ctx (the RPC's own context, which stays live
// for the whole Evaluate call) is done.
func newInputBuilder(gitClient gitv1.GitServiceClient) gates.InputBuilder {
	return func(ctx context.Context, runID uuid.UUID, head gates.RunHead, gate string, params map[string]any) (gates.Input, error) {
		repoName, err := resolveRepoName(ctx, gitClient, head.RepoID)
		if err != nil {
			return gates.Input{}, err
		}

		workdir, err := os.MkdirTemp("", "nf-gate-"+gate+"-*")
		if err != nil {
			return gates.Input{}, fmt.Errorf("create workspace: %w", err)
		}
		go func() {
			<-ctx.Done()
			_ = os.RemoveAll(workdir)
		}()

		if err := materializeTree(ctx, gitClient, repoName, head.HeadSHA, "", workdir); err != nil {
			return gates.Input{}, fmt.Errorf("materialize workspace for %s@%s: %w", repoName, head.HeadSHA, err)
		}

		return gates.Input{
			OrgID:     head.OrgID,
			RepoID:    head.RepoID,
			RunID:     runID,
			WorkDir:   workdir,
			TargetSHA: head.HeadSHA,
			SourceSHA: head.HeadSHA,
			Params:    params,
			Exec:      analysis.DefaultExec,
			SASTRules: os.Getenv("NOVAFORGE_SEMGREP_RULES"),
		}, nil
	}
}

// materializeTree recursively checks out ref's tree at path into destDir by
// walking GetTree and fetching each blob's content with GetBlob.
func materializeTree(ctx context.Context, gitClient gitv1.GitServiceClient, repo, ref, path, destDir string) error {
	treeResp, err := gitClient.GetTree(ctx, &gitv1.GetTreeRequest{Repo: repo, Ref: ref, Path: path})
	if err != nil {
		return fmt.Errorf("get tree %q: %w", path, err)
	}
	for _, entry := range treeResp.GetEntries() {
		entryPath := entry.GetName()
		if path != "" {
			entryPath = path + "/" + entry.GetName()
		}
		destPath := filepath.Join(destDir, entryPath)
		switch entry.GetKind() {
		case "tree":
			if err := os.MkdirAll(destPath, 0o755); err != nil {
				return fmt.Errorf("mkdir %q: %w", entryPath, err)
			}
			if err := materializeTree(ctx, gitClient, repo, ref, entryPath, destDir); err != nil {
				return err
			}
		case "blob":
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return fmt.Errorf("mkdir parent of %q: %w", entryPath, err)
			}
			blobResp, err := gitClient.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repo, Ref: ref, Path: entryPath})
			if err != nil {
				return fmt.Errorf("get blob %q: %w", entryPath, err)
			}
			if err := os.WriteFile(destPath, blobResp.GetContent(), 0o644); err != nil {
				return fmt.Errorf("write %q: %w", entryPath, err)
			}
		}
	}
	return nil
}

// authInterceptor resolves the caller from the request's "authorization"
// metadata via the identity service and attaches the resulting authz.Scope
// to the request context. See cmd/git-platform/main.go, which this mirrors
// exactly.
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
