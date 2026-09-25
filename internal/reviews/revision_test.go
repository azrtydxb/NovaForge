package reviews_test

import (
	"context"
	"encoding/json"
	"github.com/azrtydxb/go-ai-sdk/provider"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/reviews"
	"google.golang.org/grpc"
)

const reviewedSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type revisionGit struct {
	stubMergeGitClient
	head    string
	request *gitv1.MergeRequest
}

func (g *revisionGit) ListCommits(context.Context, *gitv1.ListCommitsRequest, ...grpc.CallOption) (*gitv1.ListCommitsResponse, error) {
	return &gitv1.ListCommitsResponse{Commits: []*gitv1.Commit{{Sha: g.head}}}, nil
}
func (g *revisionGit) Merge(ctx context.Context, req *gitv1.MergeRequest, opts ...grpc.CallOption) (*gitv1.MergeResponse, error) {
	g.request = req
	return g.stubMergeGitClient.Merge(ctx, req, opts...)
}

func TestMergeRejectsLegacyUnboundApproval(t *testing.T) {
	store := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run, err := store.CreateRun(ctx, reviews.Run{OrgID: org, RepoID: uuid.New(), Title: "unbound", SourceRef: "src", TargetRef: "main", AuthorID: uuid.New(), AuthorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SubmitReview(ctx, run.ID, uuid.New(), "user", "approve"); err != nil {
		t.Fatal(err)
	}
	git := &revisionGit{head: reviewedSHA}
	m := reviews.Merger{Store: store, Gates: stubGateChecker{allowed: true}, Git: git}
	if _, err := m.Merge(ctx, run.ID, "merge"); err == nil || git.called {
		t.Fatalf("unbound approval authorized merge: error=%v git=%v", err, git.called)
	}
}

func TestReviewRevisionAndSummarySurviveRPCAndChangedHeadCannotMerge(t *testing.T) {
	store := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run, err := store.CreateRun(ctx, reviews.Run{OrgID: org, RepoID: uuid.New(), Title: "revision", SourceRef: "src", TargetRef: "main", AuthorID: uuid.New(), AuthorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	git := &revisionGit{head: reviewedSHA}
	srv := reviews.NewGRPCServer(store)
	srv.Git = git
	req := &reviewsv1.SubmitReviewRequest{RunId: run.ID.String(), Verdict: "approve", ExpectedSourceSha: reviewedSHA, Summary: "inspected exact code"}
	req.ExpectedSourceSha = "older"
	if _, err := srv.SubmitReview(ctx, req); status.Code(err) != codes.Aborted {
		t.Fatalf("stale review: %v", err)
	}
	req.ExpectedSourceSha = reviewedSHA
	if _, err := srv.SubmitReview(ctx, req); err != nil {
		t.Fatal(err)
	}
	list, err := srv.ListReviews(ctx, &reviewsv1.ListReviewsRequest{RunId: run.ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.GetReviews()) != 1 || list.GetReviews()[0].GetSummary() != req.Summary || list.GetReviews()[0].GetSourceSha() != reviewedSHA || list.GetCurrentSourceSha() != reviewedSHA {
		t.Fatalf("lost review evidence: %v", list)
	}
	if _, err := srv.ListReviews(scopedCtx(uuid.New()), &reviewsv1.ListReviewsRequest{RunId: run.ID.String()}); err == nil {
		t.Fatal("cross-org history exposed")
	}
	m := reviews.Merger{Store: store, Gates: stubGateChecker{allowed: true}, Git: git}
	git.head = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := m.Merge(ctx, run.ID, "merge"); err == nil || git.called {
		t.Fatalf("stale approval authorized new head: %v", err)
	}
	git.head = reviewedSHA
	req.Verdict = "request_changes"
	if _, err := srv.SubmitReview(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Merge(ctx, run.ID, "merge"); err == nil || git.called {
		t.Fatal("newer request_changes retained earlier approval")
	}
	req.Verdict = "approve"
	if _, err := srv.SubmitReview(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Merge(ctx, run.ID, "merge"); err != nil {
		t.Fatal(err)
	}
	if git.request.GetExpectedSourceSha() != reviewedSHA {
		t.Fatal("Git merge omitted atomic expected source")
	}
}

type inspectingModel struct {
	stubReviewModel
	inspect func(provider.Call)
}

func (m *inspectingModel) Generate(ctx context.Context, c provider.Call) (*provider.Response, error) {
	m.inspect(c)
	return m.stubReviewModel.Generate(ctx, c)
}

type inspectingGit struct {
	revisionGit
	diffRequest *gitv1.GetDiffRequest
}

func (g *inspectingGit) GetDiff(_ context.Context, r *gitv1.GetDiffRequest, _ ...grpc.CallOption) (*gitv1.GetDiffResponse, error) {
	g.diffRequest = r
	return &gitv1.GetDiffResponse{Unified: "+this exact change was reviewed"}, nil
}
func TestAgentReviewInspectsDiffAndKeepsPreModelRevision(t *testing.T) {
	store := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run := runWithAuthorAgent(t, store, ctx, org, uuid.New())
	git := &inspectingGit{revisionGit: revisionGit{head: reviewedSHA}}
	called := false
	model := &inspectingModel{stubReviewModel: *approveModel("review-model"), inspect: func(c provider.Call) {
		called = true
		raw, _ := json.Marshal(c.Messages)
		if !strings.Contains(string(raw), "this exact change was reviewed") || !strings.Contains(string(raw), reviewedSHA) {
			t.Fatalf("model was not shown the pinned diff: %s", raw)
		}
		git.head = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	}}
	r := reviews.AgentReviewer{Store: store, Git: git, RoleAgent: map[string]agents.Agent{"reviewer": {ID: uuid.New(), Name: "independent"}}, Models: []provider.LanguageModel{model}}
	if _, err := r.ReviewRun(ctx, run.ID, []string{"reviewer"}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListReviews(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !called || git.diffRequest.GetTo() != reviewedSHA || len(rows) != 1 || rows[0].SourceSHA != reviewedSHA {
		t.Fatalf("review attached to uninspected revision: %#v %v", rows, git.diffRequest)
	}
}

func TestExceptionsRequireCurrentEligibleReview(t *testing.T) {
	store := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run := newExceptionRun(t, store, org, "user")
	if err := store.RecordProof(ctx, run.ID, "tests", "pass", "passed"); err != nil {
		t.Fatal(err)
	}
	reviewer := uuid.New()
	if err := store.SubmitReview(ctx, run.ID, reviewer, "user", "approve"); err != nil {
		t.Fatal(err)
	}
	srv := reviews.NewGRPCServer(store)
	git := &revisionGit{head: reviewedSHA}
	srv.Git = git
	check := func(want int32) {
		t.Helper()
		out, err := srv.GetExceptions(ctx, &reviewsv1.GetExceptionsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if out.GetSummary().GetReadyToAutoMerge() != want {
			t.Fatalf("ready=%d want=%d (%v)", out.GetSummary().GetReadyToAutoMerge(), want, out)
		}
	}
	check(0)
	if err := store.SubmitReviewAt(ctx, run.ID, reviewer, "user", "approve", reviewedSHA, "inspected"); err != nil {
		t.Fatal(err)
	}
	check(1)
	git.head = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	check(0)
	git.head = ""
	check(0)
	srv.Git = nil
	check(0)
}

func TestListReviewsSurvivesUnavailableSource(t *testing.T) {
	store := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run := newExceptionRun(t, store, org, "user")
	if err := store.SubmitReviewAt(ctx, run.ID, uuid.New(), "user", "approve", reviewedSHA, "durable summary"); err != nil {
		t.Fatal(err)
	}
	srv := reviews.NewGRPCServer(store)
	out, err := srv.ListReviews(ctx, &reviewsv1.ListReviewsRequest{RunId: run.ID.String()})
	if err != nil {
		t.Fatalf("Git outage hid durable reviews: %v", err)
	}
	if out.GetCurrentSourceSha() != "" || len(out.GetReviews()) != 1 || out.GetReviews()[0].GetSummary() != "durable summary" || out.GetReviews()[0].GetSourceSha() != reviewedSHA {
		t.Fatalf("lost evidence or invented revision: %v", out)
	}
}

type hangingRevisionGit struct{ gitv1.GitServiceClient }

func (hangingRevisionGit) ListCommits(ctx context.Context, _ *gitv1.ListCommitsRequest, _ ...grpc.CallOption) (*gitv1.ListCommitsResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestListReviewsBoundsAdvisoryGitLookup(t *testing.T) {
	store := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run := newExceptionRun(t, store, org, "user")
	srv := reviews.NewGRPCServer(store)
	srv.Git = hangingRevisionGit{}
	// The caller deadline is only a test safety net. The owner must time out
	// its advisory probe first, while the caller can still read the response.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := srv.ListReviews(ctx, &reviewsv1.ListReviewsRequest{RunId: run.ID.String()})
	if err != nil || ctx.Err() != nil || out.GetCurrentSourceAvailable() || out.GetCurrentSourceSha() != "" {
		t.Fatalf("hung Git hid history: %v %v caller=%v", out, err, ctx.Err())
	}
}
