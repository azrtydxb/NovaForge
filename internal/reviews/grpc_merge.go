package reviews

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
)

// ListProof returns a run's per-gate proof — the evidence an Engineering Run
// presents alongside its diff, which is the whole point of calling it a run
// rather than a pull request. The run may be addressed by id or by the
// (repo_id, number) pair people use.
func (g *GRPCServer) ListProof(ctx context.Context, req *reviewsv1.ListProofRequest) (*reviewsv1.ListProofResponse, error) {
	run, err := g.resolveRun(ctx, req.GetRunId(), req.GetRepoId(), req.GetNumber())
	if err != nil {
		return nil, err
	}
	records, err := g.Store.ListProof(ctx, run.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list proof: %v", err)
	}
	out := make([]*reviewsv1.ProofRecord, 0, len(records))
	for _, p := range records {
		out = append(out, &reviewsv1.ProofRecord{
			Gate:       p.Gate,
			Status:     p.Status,
			Detail:     p.Detail,
			RecordedAt: p.RecordedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return &reviewsv1.ListProofResponse{Proof: out}, nil
}

// MergeRun merges a run through Merger, which asks the gate controller
// before any git operation. A refused merge is reported as a failed
// precondition naming the reasons, not as an internal error: the caller
// asked a legitimate question and the answer is no.
func (g *GRPCServer) MergeRun(ctx context.Context, req *reviewsv1.MergeRunRequest) (*reviewsv1.MergeRunResponse, error) {
	if g.Merger == nil {
		return nil, status.Error(codes.Unimplemented, "this deployment has no merge path configured")
	}
	run, err := g.resolveRun(ctx, req.GetRunId(), req.GetRepoId(), req.GetNumber())
	if err != nil {
		return nil, err
	}
	sha, err := g.Merger.Merge(ctx, run.ID, req.GetMethod())
	if err != nil {
		if errors.Is(err, ErrMergeBlocked) {
			return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
		}
		return nil, status.Errorf(codes.Internal, "merge: %v", err)
	}
	return &reviewsv1.MergeRunResponse{MergeSha: sha}, nil
}

// resolveRun accepts either spelling of a run's address and checks it
// belongs to the caller's organization, so the two RPCs above cannot each
// grow their own slightly different version of that check.
func (g *GRPCServer) resolveRun(ctx context.Context, id, repoID string, number int32) (Run, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return Run{}, err
	}
	var run Run
	switch {
	case id != "":
		runID, perr := parseUUID("run_id", id)
		if perr != nil {
			return Run{}, perr
		}
		run, err = g.Store.GetRun(ctx, runID)
	case repoID != "" && number > 0:
		rid, perr := parseUUID("repo_id", repoID)
		if perr != nil {
			return Run{}, perr
		}
		run, err = g.Store.GetRunByNumber(ctx, rid, int(number))
	default:
		return Run{}, status.Error(codes.InvalidArgument, "one of run_id, or repo_id with a positive number, is required")
	}
	if err != nil {
		return Run{}, status.Errorf(codes.NotFound, "get run: %v", err)
	}
	if run.OrgID != orgID {
		return Run{}, status.Error(codes.PermissionDenied, "run does not belong to this organization")
	}
	return run, nil
}
