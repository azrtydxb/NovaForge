package reviews

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
)

// ErrMergeBlocked is returned by Merger.Merge whenever the gate controller
// does not return a literal allowed==true, including when it cannot be
// reached at all. Wrap it with the specific reasons so callers can log or
// surface why.
var ErrMergeBlocked = errors.New("merge blocked by gate controller")

// GateChecker is the merge authority reviews.Merger defers to. In
// production it is backed by a gRPC client for
// novaforge.gates.v1.GatesService.MayMerge (or, before that service exists,
// directly by a *gates.Controller in the same process, which has an
// identical method signature); Merger has no other path to a merge
// decision. Its signature matches gates.Controller.MayMerge exactly so
// either can satisfy it.
type GateChecker interface {
	MayMerge(ctx context.Context, runID uuid.UUID) (allowed bool, reasons []string, err error)
}

// Merger merges Engineering Runs only through the gate controller: Merge's
// very first action is the MayMerge check, and every non-true result —
// including a transport error reaching the controller — returns before any
// git operation is attempted. This is what makes gate enforcement
// structural rather than a convention an agent could skip.
type Merger struct {
	Store *Store
	Gates GateChecker
	Git   gitv1.GitServiceClient
}

// Merge merges runID's source ref into its target ref using method (one of
// "merge", "squash", or "rebase"), returning the resulting merge SHA.
func (m *Merger) Merge(ctx context.Context, runID uuid.UUID, method string) (string, error) {
	allowed, reasons, err := m.Gates.MayMerge(ctx, runID)
	if err != nil {
		return "", fmt.Errorf("%w: gate controller unreachable: %v", ErrMergeBlocked, err)
	}
	if !allowed {
		return "", fmt.Errorf("%w: %s", ErrMergeBlocked, strings.Join(reasons, "; "))
	}

	run, err := m.Store.GetRun(ctx, runID)
	if err != nil {
		return "", fmt.Errorf("get run %s: %w", runID, err)
	}

	independentlyApproved, err := m.Store.hasIndependentApproval(ctx, runID, run.AuthorID)
	if err != nil {
		return "", err
	}
	if !independentlyApproved {
		return "", fmt.Errorf("%w: run %s has no approval independent of its author", ErrMergeBlocked, runID)
	}

	resp, err := m.Git.Merge(ctx, &gitv1.MergeRequest{
		Repo:      run.RepoID.String(),
		SourceRef: run.SourceRef,
		TargetRef: run.TargetRef,
		Method:    method,
	})
	if err != nil {
		return "", fmt.Errorf("merge run %s: %w", runID, err)
	}

	if err := m.Store.setRunState(ctx, runID, "merged"); err != nil {
		return "", err
	}

	return resp.MergeSha, nil
}

// hasIndependentApproval reports whether runID has at least one recorded
// review with verdict "approve" from someone other than authorID.
// reviews.Store.SubmitReview already refuses to record a self-approval, so
// this is the only place an "approved" run can ever come from someone other
// than its author.
func (s *Store) hasIndependentApproval(ctx context.Context, runID, authorID uuid.UUID) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM reviews.run_reviews
		WHERE run_id = $1 AND reviewer_id <> $2 AND verdict = 'approve'`,
		runID, authorID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check independent approval for run %s: %w", runID, err)
	}
	return count > 0, nil
}

// setRunState transitions runID to state (e.g. "merged").
func (s *Store) setRunState(ctx context.Context, runID uuid.UUID, state string) error {
	_, err := s.pool.Exec(ctx, `UPDATE reviews.runs SET state = $1 WHERE id = $2`, state, runID)
	if err != nil {
		return fmt.Errorf("set run %s state to %q: %w", runID, state, err)
	}
	return nil
}
