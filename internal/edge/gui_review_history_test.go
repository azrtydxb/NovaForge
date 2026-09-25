package edge_test

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/edge"
	"github.com/novaforge/novaforge/internal/platformtest"
	"github.com/novaforge/novaforge/internal/reviews"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"net/http"
)

type historyReviewsClient struct {
	reviewsv1.ReviewsServiceClient
	server *reviews.GRPCServer
}

func (c historyReviewsClient) ListReviews(ctx context.Context, r *reviewsv1.ListReviewsRequest, _ ...grpc.CallOption) (*reviewsv1.ListReviewsResponse, error) {
	return c.server.ListReviews(ctx, r)
}

type historyGitClient struct {
	gitv1.GitServiceClient
	unavailable atomic.Bool
}

func (c *historyGitClient) ListCommits(ctx context.Context, r *gitv1.ListCommitsRequest, o ...grpc.CallOption) (*gitv1.ListCommitsResponse, error) {
	if c.unavailable.Load() {
		return nil, status.Error(codes.Unavailable, "owned test Git unavailable")
	}
	return c.GitServiceClient.ListCommits(ctx, r, o...)
}

// The owner and datastore are real; the test composes the new handler without
// claiming parent startup/OpenAPI registration. Without route registration this
// intentionally remains red, pinning the required parent integration seam.
func TestGUIReviewHistoryCanonicalIdentitySurvivesGitLoss(t *testing.T) {
	guiDatabase(t)
	p := platformtest.Start(t)
	owner := p.NewUser(t, "history")
	org := p.NewOrg(t, owner, "historyorg")
	repo := p.NewRepo(t, owner, org, "historyrepo", nil)
	ctx := p.AsUser(owner, org)
	if _, err := p.Git.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: repo.ID, Name: "feature", FromRef: "main"}); err != nil {
		t.Fatal(err)
	}
	scope := authz.WithScope(context.Background(), authz.Scope{OrgID: uuid.MustParse(org.ID), ActorID: uuid.MustParse(owner.ID), ActorKind: "user"})
	store := reviews.NewStore(p.Pool)
	run, err := store.CreateRun(scope, reviews.Run{OrgID: uuid.MustParse(org.ID), RepoID: uuid.MustParse(repo.ID), Title: "durable history", SourceRef: "feature", TargetRef: "main", AuthorID: uuid.New(), AuthorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SubmitReviewAt(scope, run.ID, uuid.MustParse(owner.ID), "user", "approve", repo.Head, "persisted inspected summary"); err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(p.GitAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithUnaryInterceptor(edge.ForwardCredential))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	git := &historyGitClient{GitServiceClient: gitv1.NewGitServiceClient(conn)}
	server := reviews.NewGRPCServer(store)
	server.Git = git
	handlers := map[string]http.HandlerFunc{}
	edge.AddGUIHandlers(handlers, nil, nil, historyReviewsClient{server: server}, nil)
	httpServer := httptest.NewServer(edge.NewRouter(edge.Config{Identity: p.Identity, Handlers: handlers}))
	t.Cleanup(httpServer.Close)
	base := httpServer.URL + "/api/v1/orgs/" + org.Name + "/engineering-runs/"
	history := base + run.ID.String() + "/reviews"
	check := func(available bool) {
		t.Helper()
		body := guiRequest(t, "GET", history, owner.Session, nil, 200)
		rows := body["reviews"].([]any)
		if len(rows) != 1 || rows[0].(map[string]any)["summary"] != "persisted inspected summary" || rows[0].(map[string]any)["source_sha"] != repo.Head || body["current_source_available"] != available {
			t.Fatalf("lost history/currency: %v", body)
		}
		if !available && body["current_source_sha"] != "" {
			t.Fatalf("invented source: %v", body)
		}
	}
	check(true)
	// Delete only this fixture's owned source ref, through real Git.
	bare := filepath.Join(p.GitRoot, org.ID, repo.Name+".git")
	if out, err := exec.Command("git", "--git-dir", bare, "update-ref", "-d", "refs/heads/feature").CombinedOutput(); err != nil {
		t.Fatalf("delete owned source: %s %v", out, err)
	}
	check(false)
	git.unavailable.Store(true)
	check(false)
	guiRequest(t, "GET", base+uuid.NewString()+"/reviews", owner.Session, nil, 404)
	guiRequest(t, "GET", base+"invalid/reviews", owner.Session, nil, 400)
	expired, err := p.Identity.CreateToken(ctx, &identityv1.CreateTokenRequest{Name: "owned-expiry-regression", Scopes: []string{"repo:read"}, TtlSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	// The public timestamp has second precision; it may precede the stored
	// expiry. Wait the requested lifetime after issuance, not that rounded time.
	time.Sleep(1100 * time.Millisecond)
	guiRequest(t, "GET", history, expired.GetPlaintext(), nil, 401)
	guiRequest(t, "GET", history, "invalid-credential", nil, 401)
	guiRequest(t, "GET", history, "", nil, 401)
	otherOrg := p.NewOrg(t, owner, "otherhistory")
	guiRequest(t, "GET", httpServer.URL+"/api/v1/orgs/"+otherOrg.Name+"/engineering-runs/"+run.ID.String()+"/reviews", owner.Session, nil, 404)
}

func TestGUIExceptionsFollowRealSourcePush(t *testing.T) {
	guiDatabase(t)
	p := platformtest.Start(t)
	owner := p.NewUser(t, "readiness")
	org := p.NewOrg(t, owner, "readinessorg")
	repo := p.NewRepo(t, owner, org, "readinessrepo", nil)
	ctx := authz.WithScope(p.AsUser(owner, org), authz.Scope{OrgID: uuid.MustParse(org.ID), ActorID: uuid.MustParse(owner.ID), ActorKind: "user"})
	if _, err := p.Git.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: repo.ID, Name: "feature", FromRef: "main"}); err != nil {
		t.Fatal(err)
	}
	store := reviews.NewStore(p.Pool)
	srv := reviews.NewGRPCServer(store)
	srv.Git = p.Git
	run, err := store.CreateRun(ctx, reviews.Run{OrgID: uuid.MustParse(org.ID), RepoID: uuid.MustParse(repo.ID), Title: "readiness", SourceRef: "feature", TargetRef: "main", AuthorID: uuid.New(), AuthorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordProof(ctx, run.ID, "tests", "pass", "passing proof"); err != nil {
		t.Fatal(err)
	}
	reviewer := uuid.MustParse(owner.ID)
	if err := store.SubmitReview(ctx, run.ID, reviewer, "user", "approve"); err != nil {
		t.Fatal(err)
	}
	check := func(want int32) {
		t.Helper()
		out, err := srv.GetExceptions(ctx, &reviewsv1.GetExceptionsRequest{})
		if err != nil || out.GetSummary().GetReadyToAutoMerge() != want {
			t.Fatalf("real source readiness: %v %v want=%d", out, err, want)
		}
	}
	check(0)
	if err := store.SubmitReviewAt(ctx, run.ID, reviewer, "user", "approve", repo.Head, "inspected original"); err != nil {
		t.Fatal(err)
	}
	check(1)
	commit, err := p.Git.CreateCommit(ctx, &gitv1.CreateCommitRequest{Repo: repo.ID, Branch: "feature", Message: "move source after review", AuthorName: owner.Username, AuthorEmail: "fixture@example.test", Files: []*gitv1.FileChange{{Path: "changed.txt", Content: []byte("unreviewed change\n")}}})
	if err != nil {
		t.Fatal(err)
	}
	check(0)
	if err := store.SubmitReviewAt(ctx, run.ID, reviewer, "user", "approve", commit.GetSha(), "inspected changed source"); err != nil {
		t.Fatal(err)
	}
	check(1)
	srv.Git = &historyGitClient{GitServiceClient: p.Git}
	srv.Git.(*historyGitClient).unavailable.Store(true)
	check(0)
}
