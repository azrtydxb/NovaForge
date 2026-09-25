package edge_test

import (
	"context"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/edge"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/reviews"
)

// TestEngineeringRunProof is spec S-5's criterion through the REST API: an
// Engineering Run exposes its plan, its change impact measured from a real
// diff, its per-gate proof, and the agent and model that produced it.
//
// The run is opened the way production opens one for an agent — by
// agentrun.OpenEngineeringRun when an Agent Run succeeds — against a real
// repository in the real git service, and read back through the edge. Before
// this nothing opened a run for an agent at all, nothing recorded a plan, the
// agent's model reached no run, and change impact had no route.
func TestEngineeringRunProof(t *testing.T) {
	pool, url := liveDB(t)
	migrateLive(t, url, "gitplatform", "gitplatform_git", gitops.MigrationsFS)
	migrateLive(t, url, "reviews", "", reviews.MigrationsFS)

	orgID, owner, agentID := uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM reviews.runs WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM gitplatform.repositories WHERE org_id = $1`, orgID)
	})

	scope := authz.Scope{OrgID: orgID, ActorID: owner, ActorKind: "user", Role: "owner"}
	reviewsServer := reviews.NewGRPCServer(reviews.NewStore(pool))
	conn := serveScoped(t, &scope, func(s *grpc.Server) {
		gitv1.RegisterGitServiceServer(s, gitops.NewGRPCServer(pool, t.TempDir()))
		reviewsv1.RegisterReviewsServiceServer(s, reviewsServer)
	})
	git := gitv1.NewGitServiceClient(conn)
	rv := reviewsv1.NewReviewsServiceClient(conn)
	reviewsServer.Git = git
	ctx := context.Background()

	repo, err := git.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: "platform"})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	commit := func(branch string, files map[string]string) {
		t.Helper()
		var changes []*gitv1.FileChange
		for p, body := range files {
			changes = append(changes, &gitv1.FileChange{Path: p, Content: []byte(body)})
		}
		if _, err := git.CreateCommit(ctx, &gitv1.CreateCommitRequest{
			Repo: "platform", Branch: branch, Message: "change " + branch, Files: changes,
			AuthorName: "T", AuthorEmail: "t@example.com",
		}); err != nil {
			t.Fatalf("CreateCommit on %s: %v", branch, err)
		}
	}
	commit("main", map[string]string{"README.md": "platform\n"})
	const branch = "agents/NF-7/work"
	if _, err := git.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: "platform", Name: branch}); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	commit(branch, map[string]string{
		"api/limit.go":      "package api\n\nfunc Limit() int { return 100 }\n",
		"api/limit_test.go": "package api\n\nimport \"testing\"\n\nfunc TestLimit(t *testing.T) {}\n",
	})
	// main moves on after the branch is cut. The run's change is still two
	// files: a plain two-point diff would count this one too, in reverse.
	commit("main", map[string]string{"docs/CHANGELOG.md": "unrelated\n"})

	// The Agent Run succeeded; production opens its Engineering Run as the agent.
	scope = authz.Scope{OrgID: orgID, ActorID: agentID, ActorKind: "agent"}
	spec := agentrun.EngineeringRunSpec{
		RepoID: uuid.MustParse(repo.GetRepo().GetId()), WorkItemID: uuid.New(), WorkItemKey: "NF-7",
		Goal:       "Rate-limit the public API",
		Acceptance: []string{"429 after 100 requests a minute", "limits are per token"},
		Branch:     branch, AgentID: agentID, AgentName: "coder", ModelName: "qwen3-6-35b-a3b",
	}
	opened, err := agentrun.OpenEngineeringRun(ctx, rv, git, spec)
	if err != nil || opened == nil {
		t.Fatalf("OpenEngineeringRun: %v (run %v)", err, opened)
	}
	again, err := agentrun.OpenEngineeringRun(ctx, rv, git, spec)
	if err != nil || again == nil || again.GetNumber() != opened.GetNumber() {
		t.Fatalf("a second open for the same branch made another run: %v, %v", again, err)
	}
	if _, err := authenticatedProofClient(t, reviewsServer, orgID).RecordProof(context.Background(), &reviewsv1.RecordProofRequest{RunId: opened.GetId(), Gate: "tests", Status: "pass", Detail: "ok"}); err != nil {
		t.Fatalf("RecordProof: %v", err)
	}

	// A person reads it all through the API.
	scope = authz.Scope{OrgID: orgID, ActorID: owner, ActorKind: "user", Role: "owner"}
	h := edge.Handlers(edge.Config{Git: git, Reviews: rv})
	params := map[string]string{"org": "acme", "repo": "platform", "number": strconv.Itoa(int(opened.GetNumber()))}

	rec := call(t, h, "getRun", http.MethodGet, "", params)
	if rec.Code != http.StatusOK {
		t.Fatalf("getRun: %d %s", rec.Code, rec.Body.String())
	}
	var run struct {
		AuthorKind string `json:"author_kind"`
		AuthorID   string `json:"author_id"`
		AgentName  string `json:"agent_name"`
		ModelName  string `json:"model_name"`
		SourceRef  string `json:"source_ref"`
		TargetRef  string `json:"target_ref"`
		WorkItemID string `json:"work_item_id"`
	}
	decodeJSON(t, rec.Body.Bytes(), &run)
	if run.AuthorKind != "agent" || run.AuthorID != agentID.String() || run.AgentName != "coder" || run.ModelName != "qwen3-6-35b-a3b" {
		t.Fatalf("run provenance = %+v, want the agent coder on qwen3-6-35b-a3b", run)
	}
	if run.SourceRef != branch || run.TargetRef != "main" || run.WorkItemID != spec.WorkItemID.String() {
		t.Fatalf("run refs = %+v", run)
	}

	rec = call(t, h, "getRunPlan", http.MethodGet, "", params)
	var plan struct {
		Steps []struct {
			Ordinal int    `json:"ordinal"`
			Text    string `json:"text"`
			State   string `json:"state"`
		} `json:"steps"`
	}
	decodeJSON(t, rec.Body.Bytes(), &plan)
	if len(plan.Steps) != 2 || plan.Steps[0].Text != spec.Acceptance[0] || plan.Steps[1].Text != spec.Acceptance[1] {
		t.Fatalf("plan = %+v, want the work item's two criteria", plan.Steps)
	}

	rec = call(t, h, "getRunImpact", http.MethodGet, "", params)
	if rec.Code != http.StatusOK {
		t.Fatalf("getRunImpact: %d %s", rec.Code, rec.Body.String())
	}
	var impact struct {
		FilesChanged int      `json:"files_changed"`
		Insertions   int      `json:"insertions"`
		Paths        []string `json:"paths"`
		Risk         string   `json:"risk"`
		RiskReasons  []string `json:"risk_reasons"`
	}
	decodeJSON(t, rec.Body.Bytes(), &impact)
	sort.Strings(impact.Paths)
	if impact.FilesChanged != 2 || strings.Join(impact.Paths, ",") != "api/limit.go,api/limit_test.go" {
		t.Fatalf("impact = %+v, want exactly the branch's two files", impact)
	}
	if impact.Insertions == 0 || impact.Risk != "low" || len(impact.RiskReasons) == 0 {
		t.Fatalf("impact = %+v, want insertions and a low risk with its reason", impact)
	}

	rec = call(t, h, "getRunProof", http.MethodGet, "", params)
	var proof struct {
		Proof []struct {
			Gate   string `json:"gate"`
			Status string `json:"status"`
		} `json:"proof"`
	}
	decodeJSON(t, rec.Body.Bytes(), &proof)
	if len(proof.Proof) != 1 || proof.Proof[0].Gate != "tests" || proof.Proof[0].Status != "pass" {
		t.Fatalf("proof = %+v", proof.Proof)
	}
}

// Proof producers cross the actual signed-service-token interceptor even when
// the rest of this older fixture uses a configurable user scope.
func authenticatedProofClient(t *testing.T, server *reviews.GRPCServer, org uuid.UUID) reviewsv1.ReviewsServiceClient {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(nil, "edge-proof-test")))
	reviewsv1.RegisterReviewsServiceServer(srv, server)
	go srv.Serve(listener)
	t.Cleanup(srv.Stop)
	token, err := svcauth.Mint("edge-proof-test", "gates", org, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), method, req, reply, cc, opts...)
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return reviewsv1.NewReviewsServiceClient(conn)
}
