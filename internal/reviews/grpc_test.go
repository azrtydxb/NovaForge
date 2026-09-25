package reviews_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/reviews"
)

func newGRPCServer(t *testing.T) *reviews.GRPCServer {
	t.Helper()
	return reviews.NewGRPCServer(newStore(t))
}

func TestCreateRunOverGRPC(t *testing.T) {
	srv := newGRPCServer(t)
	orgID := uuid.New()
	repoID := uuid.New()
	ctx := scopedCtx(orgID)

	resp, err := srv.CreateRun(ctx, &reviewsv1.CreateRunRequest{
		RepoId:     repoID.String(),
		Title:      "add feature",
		SourceRef:  "refs/heads/feature",
		TargetRef:  "refs/heads/main",
		AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if resp.GetRun().GetNumber() == 0 {
		t.Fatalf("CreateRun response has no allocated number: %+v", resp)
	}
	if resp.GetRun().GetState() == "" {
		t.Fatalf("CreateRun response has no state: %+v", resp)
	}
}

// TestGetRunDeniedForForeignOrg asserts a run created in one organization
// cannot be read by a caller scoped to a different organization.
func TestGetRunDeniedForForeignOrg(t *testing.T) {
	srv := newGRPCServer(t)
	orgA := uuid.New()
	orgB := uuid.New()
	repoID := uuid.New()

	created, err := srv.CreateRun(scopedCtx(orgA), &reviewsv1.CreateRunRequest{
		RepoId:     repoID.String(),
		Title:      "org a's run",
		SourceRef:  "refs/heads/feature",
		TargetRef:  "refs/heads/main",
		AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// The store itself scopes GetRun's query by the caller's org, so a
	// cross-org lookup surfaces as NotFound rather than PermissionDenied —
	// indistinguishable from the run simply not existing, which is the
	// correct behavior for an org boundary (it does not confirm the run's
	// existence to a caller outside its organization).
	_, err = srv.GetRun(scopedCtx(orgB), &reviewsv1.GetRunRequest{Id: created.GetRun().GetId()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("GetRun across orgs: code = %v, want NotFound", status.Code(err))
	}
}

// TestAddCommentDeniedForForeignOrg asserts a mutation against a run in
// another organization is refused, not just reads.
func TestAddCommentDeniedForForeignOrg(t *testing.T) {
	srv := newGRPCServer(t)
	orgA := uuid.New()
	orgB := uuid.New()
	repoID := uuid.New()

	created, err := srv.CreateRun(scopedCtx(orgA), &reviewsv1.CreateRunRequest{
		RepoId:     repoID.String(),
		Title:      "org a's run",
		SourceRef:  "refs/heads/feature",
		TargetRef:  "refs/heads/main",
		AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Same reasoning as GetRun: requireRunInOrg's lookup is itself
	// org-scoped, so a run in another organization is invisible (NotFound)
	// rather than visible-but-refused.
	_, err = srv.AddComment(scopedCtx(orgB), &reviewsv1.AddCommentRequest{
		RunId:      created.GetRun().GetId(),
		AuthorId:   uuid.New().String(),
		AuthorKind: "user",
		Body:       "nice work",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("AddComment across orgs: code = %v, want NotFound", status.Code(err))
	}
}

// TestListRunsRequiresScope asserts a call with no authorization scope in
// context is denied rather than defaulting to some unrestricted view.
func TestListRunsRequiresScope(t *testing.T) {
	srv := newGRPCServer(t)
	_, err := srv.ListRuns(context.Background(), &reviewsv1.ListRunsRequest{RepoId: uuid.New().String()})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ListRuns with no scope: code = %v, want PermissionDenied", status.Code(err))
	}
}

// TestSubmitReviewIsByTheCaller pins that a review is recorded as the person
// who submitted it. reviewer_id used to be taken from the request, so the GUI
// (which sends none) could not record a verdict at all, and a caller who did
// send one could review under anyone's name — including approving a run as
// someone other than its author when they were the author.
func TestSubmitReviewIsByTheCaller(t *testing.T) {
	srv := newGRPCServer(t)
	srv.Git = &stubMergeGitClient{}
	orgID := uuid.New()
	author := uuid.New()
	authorCtx := authz.WithScope(context.Background(), authz.Scope{OrgID: orgID, ActorID: author, ActorKind: "user"})
	run, err := srv.Store.CreateRun(authorCtx, reviews.Run{
		OrgID: orgID, RepoID: uuid.New(), Title: "t", SourceRef: "feature", TargetRef: "main",
		AuthorID: author, AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// No reviewer_id, as the GUI sends it: recorded as the caller.
	reviewer := uuid.New()
	reviewerCtx := authz.WithScope(context.Background(), authz.Scope{OrgID: orgID, ActorID: reviewer, ActorKind: "user"})
	if _, err := srv.SubmitReview(reviewerCtx, &reviewsv1.SubmitReviewRequest{RunId: run.ID.String(), Verdict: "approve", ExpectedSourceSha: reviewedSHA}); err != nil {
		t.Fatalf("SubmitReview without reviewer_id: %v", err)
	}

	// The author naming someone else as reviewer is refused.
	_, err = srv.SubmitReview(authorCtx, &reviewsv1.SubmitReviewRequest{
		RunId: run.ID.String(), Verdict: "approve", ReviewerId: uuid.NewString(), ReviewerKind: "user",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("author reviewing under another id: code = %v, want PermissionDenied", status.Code(err))
	}
	// And reviewing as themselves still hits the self-approval rule.
	if _, err := srv.SubmitReview(authorCtx, &reviewsv1.SubmitReviewRequest{RunId: run.ID.String(), Verdict: "approve", ExpectedSourceSha: reviewedSHA}); err == nil {
		t.Fatal("the author approved their own run")
	}
}
