package edge_test

import (
	"context"
	"net"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/edge"
	"github.com/novaforge/novaforge/internal/platformtest"
	"github.com/novaforge/novaforge/internal/reviews"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Real cookie/session authentication, RPC owners, Git and PostgreSQL; only the
// external model is controlled. No route overlay or executor success adapter.
func TestAgentReviewQueueProductionComposition(t *testing.T) {
	guiDatabase(t)
	p := platformtest.Start(t)
	user := p.NewUser(t, "requester")
	org := p.NewOrg(t, user, "reviewqueue")
	repo := p.NewRepo(t, user, org, "reviewqueue", nil)
	userctx := p.AsUser(user, org)
	reviewer, err := p.Agents.CreateAgent(userctx, &agentsv1.CreateAgentRequest{Name: "independent", Role: "reviewer", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Git.CreateBranch(userctx, &gitv1.CreateBranchRequest{Repo: repo.ID, Name: "feature", FromRef: "main"}); err != nil {
		t.Fatal(err)
	}
	head, err := p.Git.CreateCommit(userctx, &gitv1.CreateCommitRequest{Repo: repo.ID, Branch: "feature", Message: "review input", Files: []*gitv1.FileChange{{Path: "change.txt", Content: []byte("review this\n")}}})
	if err != nil {
		t.Fatal(err)
	}
	dial := func(addr string, interceptor grpc.UnaryClientInterceptor) *grpc.ClientConn {
		t.Helper()
		c, e := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithUnaryInterceptor(interceptor))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	store := reviews.NewStore(p.Pool)
	model := &queueReviewModel{}
	worker := &reviews.ReviewWorker{Store: store, Git: p.Git, Agents: p.Agents, Gateway: model.gateway(t, store), HMACSecret: platformtest.HMACSecret, Config: reviews.ReviewConfig{WallclockSeconds: 10, MaxInputBytes: 4096, MaxOutputTokens: 256, MaxConcurrentRequests: 1, MaxRoles: 4}}
	server := reviews.NewGRPCServer(store)
	server.Git = gitv1.NewGitServiceClient(dial(p.GitAddr, svcauth.ForwardIncomingCredential))
	server.ReviewWorker = worker
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(p.Identity, platformtest.HMACSecret)))
	reviewsv1.RegisterReviewsServiceServer(grpcServer, server)
	go grpcServer.Serve(listener)
	t.Cleanup(grpcServer.Stop)
	client := reviewsv1.NewReviewsServiceClient(dial(listener.Addr().String(), edge.ForwardCredential))
	cfg := edge.Config{Identity: p.Identity, Reviews: client}
	cfg.Handlers = edge.Handlers(cfg)
	httpServer := httptest.NewServer(edge.NewRouter(cfg))
	t.Cleanup(httpServer.Close)
	// Create the run through the authenticated reviews owner, then use its known
	// canonical identity; the edge has no Git client at all.
	runClient := reviewsv1.NewReviewsServiceClient(dial(listener.Addr().String(), svcauth.ForwardIncomingCredential))
	run, err := runClient.CreateRun(userctx, &reviewsv1.CreateRunRequest{RepoId: repo.ID, Title: "independent queue", SourceRef: "feature", TargetRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	base := httpServer.URL + "/api/v1/orgs/" + org.Name + "/engineering-runs/" + run.Run.Id
	body := map[string]string{"expected_source_sha": head.Sha}
	guiRequest(t, "POST", base+"/agent-reviews", "", body, 401)
	queued := guiRequest(t, "POST", base+"/agent-reviews", user.Session, body, 202)["request"].(map[string]any)
	duplicate := guiRequest(t, "POST", base+"/agent-reviews", user.Session, body, 202)["request"].(map[string]any)
	if queued["id"] != duplicate["id"] || model.calls.Load() != 0 {
		t.Fatal("request replay invoked model or duplicated request")
	}
	model.mu.Lock()
	model.entered = make(chan struct{})
	model.release = make(chan struct{})
	model.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- worker.Tick(context.Background()) }()
	select {
	case <-model.entered:
	case <-time.After(15 * time.Second):
		t.Fatal("worker never invoked model")
	}
	// A second replica cannot steal a healthy lease or repeat the model call.
	if err := worker.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(model.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	history := guiRequest(t, "GET", base+"/agent-reviews", user.Session, nil, 200)
	request := history["requests"].([]any)[0].(map[string]any)
	attempt := request["attempts"].([]any)[0].(map[string]any)
	if request["state"] != "succeeded" || attempt["agent_id"] != reviewer.Agent.Id || attempt["tokens_available"] != true || attempt["cost_available"] != false || attempt["tokens_used"] != "15" {
		t.Fatalf("lost result/accounting: %v", history)
	}
	evidence := guiRequest(t, "GET", base+"/reviews", user.Session, nil, 200)
	if len(evidence["reviews"].([]any)) != 1 {
		t.Fatalf("no atomic independent approval: %v", evidence)
	}
	if err := worker.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if model.calls.Load() != 1 {
		t.Fatal("completed request replayed")
	}
	other := p.NewOrg(t, user, "foreignqueue")
	guiRequest(t, "GET", strings.Replace(base, org.Name, other.Name, 1)+"/agent-reviews", user.Session, nil, 404)
	// Changed source creates a distinct request. Decode failure retains measured
	// usage, never becomes a review approval, and cannot be charged by replay.
	moved, err := p.Git.CreateCommit(userctx, &gitv1.CreateCommitRequest{Repo: repo.ID, Branch: "feature", Message: "move", Files: []*gitv1.FileChange{{Path: "change.txt", Content: []byte("changed\n")}}})
	if err != nil {
		t.Fatal(err)
	}
	guiRequest(t, "POST", base+"/agent-reviews", user.Session, body, 409)
	body["expected_source_sha"] = moved.Sha
	model.mu.Lock()
	model.entered = nil
	model.release = nil
	model.mu.Unlock()
	model.fail.Store(true)
	guiRequest(t, "POST", base+"/agent-reviews", user.Session, body, 202)
	if err := worker.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	history = guiRequest(t, "GET", base+"/agent-reviews", user.Session, nil, 200)
	request = history["requests"].([]any)[0].(map[string]any)
	attempt = request["attempts"].([]any)[0].(map[string]any)
	if request["state"] != "failed" || attempt["tokens_used"] != "15" || attempt["tokens_available"] != true {
		t.Fatalf("decode failure lost measured usage: %v", history)
	}
	afterFailure := guiRequest(t, "GET", base+"/reviews", user.Session, nil, 200)
	if !reflect.DeepEqual(evidence["reviews"], afterFailure["reviews"]) {
		t.Fatal("invalid application verdict changed approval evidence")
	}
	// Simulate a process lost after durable invocation intent; only the exact
	// fixture-owned request is changed. Expiry remains uncertain, never requeued.
	id := uuid.MustParse(request["id"].(string))
	if _, err := p.Pool.Exec(context.Background(), `UPDATE reviews.agent_review_requests SET state='running',lease_until=now()-interval '1 second' WHERE org_id=$1 AND id=$2`, uuid.MustParse(org.ID), id); err != nil {
		t.Fatal(err)
	}
	if err := worker.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	history = guiRequest(t, "GET", base+"/agent-reviews", user.Session, nil, 200)
	if history["requests"].([]any)[0].(map[string]any)["state"] != "uncertain" || model.calls.Load() != 2 {
		t.Fatalf("expired lease was replayed: %v", history)
	}
}
