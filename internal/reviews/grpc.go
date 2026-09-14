package reviews

import (
	"context"
	"log"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

const rfc3339 = "2006-01-02T15:04:05.999999999Z07:00"

// GRPCServer implements reviewsv1.ReviewsServiceServer. Every method
// derives the caller's organization from authz.FromContext and passes it as
// an explicit predicate to every query — never from a field of the request
// message, so a client cannot simply name a different org and be believed.
type GRPCServer struct {
	reviewsv1.UnimplementedReviewsServiceServer

	Store *Store

	// Merger backs the MergeRun RPC — a person merging deliberately. It is
	// the same Merger AutoMerge uses, so both pass the identical gate check.
	Merger *Merger

	// AutoMerge, when set, is consulted after every review submission: a
	// run whose last blocking review has just landed is exactly the moment
	// policy can be re-evaluated. It is nil when the deployment has not
	// enabled auto-merge, and Consider itself refuses when the policy is
	// off, so this is two independent "no"s rather than one.
	AutoMerge *AutoMerger
}

// NewGRPCServer wraps store as a reviewsv1.ReviewsServiceServer.
func NewGRPCServer(store *Store) *GRPCServer {
	return &GRPCServer{Store: store}
}

func callerOrg(ctx context.Context) (uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return uuid.Nil, status.Error(codes.PermissionDenied, "no authorization scope for this call")
	}
	return scope.OrgID, nil
}

func parseUUID(field, raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, status.Errorf(codes.InvalidArgument, "invalid %s %q: %v", field, raw, err)
	}
	return id, nil
}

// optionalUUID parses raw as a uuid.UUID when non-empty, otherwise returns
// uuid.Nil with no error — several fields (work_item_id, author_id) are
// legitimately absent, e.g. a system-authored run.
func optionalUUID(field, raw string) (uuid.UUID, error) {
	if raw == "" {
		return uuid.Nil, nil
	}
	return parseUUID(field, raw)
}

func toProtoRun(r Run) *reviewsv1.Run {
	out := &reviewsv1.Run{
		Id:         r.ID.String(),
		OrgId:      r.OrgID.String(),
		RepoId:     r.RepoID.String(),
		Number:     int32(r.Number),
		Title:      r.Title,
		SourceRef:  r.SourceRef,
		TargetRef:  r.TargetRef,
		State:      r.State,
		AuthorKind: r.AuthorKind,
		AgentName:  r.AgentName,
		ModelName:  r.ModelName,
		CreatedAt:  r.CreatedAt.Format(rfc3339),
	}
	if r.WorkItemID != uuid.Nil {
		out.WorkItemId = r.WorkItemID.String()
	}
	if r.AuthorID != uuid.Nil {
		out.AuthorId = r.AuthorID.String()
	}
	return out
}

// CreateRun creates an Engineering Run within the caller's organization.
func (g *GRPCServer) CreateRun(ctx context.Context, req *reviewsv1.CreateRunRequest) (*reviewsv1.CreateRunResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	repoID, err := parseUUID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	workItemID, err := optionalUUID("work_item_id", req.GetWorkItemId())
	if err != nil {
		return nil, err
	}
	authorID, err := optionalUUID("author_id", req.GetAuthorId())
	if err != nil {
		return nil, err
	}
	run, err := g.Store.CreateRun(ctx, Run{
		OrgID:      orgID,
		RepoID:     repoID,
		WorkItemID: workItemID,
		Title:      req.GetTitle(),
		SourceRef:  req.GetSourceRef(),
		TargetRef:  req.GetTargetRef(),
		AuthorID:   authorID,
		AuthorKind: req.GetAuthorKind(),
		AgentName:  req.GetAgentName(),
		ModelName:  req.GetModelName(),
	})
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "create run: %v", err)
	}
	return &reviewsv1.CreateRunResponse{Run: toProtoRun(run)}, nil
}

// GetRun looks up a run by id, or by the (repo_id, number) pair people and
// tools use, refusing one that does not belong to the caller's organization.
func (g *GRPCServer) GetRun(ctx context.Context, req *reviewsv1.GetRunRequest) (*reviewsv1.GetRunResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	var run Run
	switch {
	case req.GetId() != "":
		id, perr := parseUUID("id", req.GetId())
		if perr != nil {
			return nil, perr
		}
		run, err = g.Store.GetRun(ctx, id)
	case req.GetRepoId() != "" && req.GetNumber() > 0:
		repoID, perr := parseUUID("repo_id", req.GetRepoId())
		if perr != nil {
			return nil, perr
		}
		run, err = g.Store.GetRunByNumber(ctx, repoID, int(req.GetNumber()))
	default:
		return nil, status.Error(codes.InvalidArgument, "one of id, or repo_id with a positive number, is required")
	}
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "get run: %v", err)
	}
	if run.OrgID != orgID {
		return nil, status.Error(codes.PermissionDenied, "run does not belong to this organization")
	}
	return &reviewsv1.GetRunResponse{Run: toProtoRun(run)}, nil
}

// ListRuns lists runs for a repository within the caller's organization,
// optionally filtered by state.
func (g *GRPCServer) ListRuns(ctx context.Context, req *reviewsv1.ListRunsRequest) (*reviewsv1.ListRunsResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	repoID, err := parseUUID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	runs, err := g.Store.ListRuns(ctx, orgID, repoID, req.GetState())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list runs: %v", err)
	}
	out := make([]*reviewsv1.Run, len(runs))
	for i, r := range runs {
		out[i] = toProtoRun(r)
	}
	return &reviewsv1.ListRunsResponse{Runs: out}, nil
}

// runOrg looks up runID's organization so every mutation below can be
// checked against the caller's scope before it touches the row.
func (g *GRPCServer) runOrg(ctx context.Context, runID uuid.UUID) (uuid.UUID, error) {
	run, err := g.Store.GetRun(ctx, runID)
	if err != nil {
		return uuid.Nil, status.Errorf(codes.NotFound, "get run: %v", err)
	}
	return run.OrgID, nil
}

func (g *GRPCServer) requireRunInOrg(ctx context.Context, runID uuid.UUID) error {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return err
	}
	actualOrg, err := g.runOrg(ctx, runID)
	if err != nil {
		return err
	}
	if actualOrg != orgID {
		return status.Error(codes.PermissionDenied, "run does not belong to this organization")
	}
	return nil
}

// AddPlanStep appends or updates a plan step on a run within the caller's
// organization.
func (g *GRPCServer) AddPlanStep(ctx context.Context, req *reviewsv1.AddPlanStepRequest) (*reviewsv1.AddPlanStepResponse, error) {
	runID, err := parseUUID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	if err := g.requireRunInOrg(ctx, runID); err != nil {
		return nil, err
	}
	if err := g.Store.AddPlanStep(ctx, runID, int(req.GetOrdinal()), req.GetText(), req.GetState()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "add plan step: %v", err)
	}
	return &reviewsv1.AddPlanStepResponse{Ok: true}, nil
}

// RecordProof records the result of one gate's evaluation against a run's
// current head, within the caller's organization.
func (g *GRPCServer) RecordProof(ctx context.Context, req *reviewsv1.RecordProofRequest) (*reviewsv1.RecordProofResponse, error) {
	runID, err := parseUUID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	if err := g.requireRunInOrg(ctx, runID); err != nil {
		return nil, err
	}
	if err := g.Store.RecordProof(ctx, runID, req.GetGate(), req.GetStatus(), req.GetDetail()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "record proof: %v", err)
	}
	return &reviewsv1.RecordProofResponse{Ok: true}, nil
}

// SubmitReview records a reviewer's verdict on a run, within the caller's
// organization.
func (g *GRPCServer) SubmitReview(ctx context.Context, req *reviewsv1.SubmitReviewRequest) (*reviewsv1.SubmitReviewResponse, error) {
	runID, err := parseUUID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	if err := g.requireRunInOrg(ctx, runID); err != nil {
		return nil, err
	}
	// A review is recorded as the person who submitted it. reviewer_id was
	// taken from the request, so the GUI (which sends none) could record no
	// verdict, and a caller who sent one could review under anyone's name —
	// an author could approve their own run as someone else. Agent reviews
	// are recorded in-process by AgentReviewer, not through this RPC.
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}
	if scope.ActorKind != "user" || scope.ActorID == uuid.Nil {
		return nil, status.Error(codes.PermissionDenied, "a review through the API is submitted by a person")
	}
	if id := req.GetReviewerId(); id != "" && id != scope.ActorID.String() {
		return nil, status.Error(codes.PermissionDenied, "a review can only be submitted as yourself")
	}
	if err := g.Store.SubmitReview(ctx, runID, scope.ActorID, "user", req.GetVerdict()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "submit review: %v", err)
	}
	g.considerAutoMerge(ctx, runID)
	return &reviewsv1.SubmitReviewResponse{Ok: true}, nil
}

// AddComment adds a comment to a run, within the caller's organization.
func (g *GRPCServer) AddComment(ctx context.Context, req *reviewsv1.AddCommentRequest) (*reviewsv1.AddCommentResponse, error) {
	runID, err := parseUUID("run_id", req.GetRunId())
	if err != nil {
		return nil, err
	}
	if err := g.requireRunInOrg(ctx, runID); err != nil {
		return nil, err
	}
	// As with SubmitReview, the author is the caller, never a request field.
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}
	if scope.ActorKind != "user" || scope.ActorID == uuid.Nil {
		return nil, status.Error(codes.PermissionDenied, "a comment through the API is written by a person")
	}
	if id := req.GetAuthorId(); id != "" && id != scope.ActorID.String() {
		return nil, status.Error(codes.PermissionDenied, "a comment can only be written as yourself")
	}
	comment, err := g.Store.AddComment(ctx, runID, scope.ActorID, "user", req.GetBody())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "add comment: %v", err)
	}
	return &reviewsv1.AddCommentResponse{Id: comment.ID.String(), CreatedAt: comment.CreatedAt.Format(rfc3339)}, nil
}

// considerAutoMerge re-evaluates auto-merge policy for runID after a review
// lands. A refusal is not an error the submitter should see — their review
// was recorded either way — so the outcome is logged, not returned.
// Consider merges only through Merger.Merge, which asks the gate controller
// first, so this can never merge something a person could not have merged.
func (g *GRPCServer) considerAutoMerge(ctx context.Context, runID uuid.UUID) {
	if g.AutoMerge == nil {
		return
	}
	merged, reason, err := g.AutoMerge.Consider(ctx, runID)
	switch {
	case err != nil:
		log.Printf("reviews: auto-merge could not evaluate run %s: %v", runID, err)
	case merged:
		log.Printf("reviews: auto-merged run %s", runID)
	case reason != "":
		log.Printf("reviews: run %s not auto-merged: %s", runID, reason)
	}
}
