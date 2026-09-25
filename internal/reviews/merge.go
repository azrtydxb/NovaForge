package reviews

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// ErrMergeBlocked is returned by Merger.Merge whenever the gate controller
// does not return a literal allowed==true, including when it cannot be
// reached at all. Wrap it with the specific reasons so callers can log or
// surface why.
var ErrMergeBlocked = errors.New("merge blocked by gate controller")

// GateChecker authorizes the exact source and policy baseline that Git must
// compare atomically. A moving-head-only authority cannot satisfy this contract.
type GateChecker interface {
	MayMergePinned(ctx context.Context, runID uuid.UUID, sourceSHA, targetSHA string) (allowed bool, reasons []string, err error)
}

// GateEvaluator is implemented by a GateChecker that can also run the gates.
// Merger uses it, when present, to evaluate a run's gates at its current head
// before asking whether it may merge.
type GateEvaluator interface {
	Evaluate(ctx context.Context, runID uuid.UUID) error
}

// Merger requires both independent review and the gate owner's authorization
// of one immutable source/target pair before a Git-native compare-and-swap.
type Merger struct {
	Store *Store
	Gates GateChecker
	Git   gitv1.GitServiceClient
}

// Merge merges runID's source ref into its target ref using method (one of
// "merge", "squash", or "rebase"), returning the resulting merge SHA.
func (m *Merger) Merge(ctx context.Context, runID uuid.UUID, method string) (string, error) {
	// Nothing else runs a run's gates. Without this, every required gate was
	// "not evaluated at head" forever and a repository that declared one could
	// never merge. The controller reuses an evaluation already made at the
	// current head, so asking again is cheap. An evaluation that cannot run
	// blocks the merge, like everything else on this path.
	if ev, ok := m.Gates.(GateEvaluator); ok {
		if err := ev.Evaluate(ctx, runID); err != nil {
			return "", fmt.Errorf("%w: gates could not be evaluated: %v", ErrMergeBlocked, err)
		}
	}
	run, err := m.Store.GetRun(ctx, runID)
	if err != nil {
		return "", fmt.Errorf("get run %s: %w", runID, err)
	}
	if run.State != "open" {
		return "", fmt.Errorf("%w: run is not open", ErrMergeBlocked)
	}
	head, err := sourceHead(ctx, m.Git, run)
	if err != nil {
		return "", fmt.Errorf("%w: resolve source: %v", ErrMergeBlocked, err)
	}
	target, err := refHead(ctx, m.Git, run.RepoID.String(), run.TargetRef)
	if err != nil {
		return "", fmt.Errorf("%w: resolve target: %v", ErrMergeBlocked, err)
	}
	if m.Gates == nil {
		return "", fmt.Errorf("%w: gate controller unavailable", ErrMergeBlocked)
	}
	allowed, reasons, err := m.Gates.MayMergePinned(ctx, runID, head, target)
	if err != nil {
		return "", fmt.Errorf("%w: gate controller unreachable: %v", ErrMergeBlocked, err)
	}
	if !allowed {
		return "", fmt.Errorf("%w: %s", ErrMergeBlocked, strings.Join(reasons, "; "))
	}

	independentlyApproved, err := m.Store.hasIndependentApproval(ctx, runID, run.AuthorID, head)
	if err != nil {
		return "", err
	}
	if !independentlyApproved {
		return "", fmt.Errorf("%w: run %s has no approval independent of its author", ErrMergeBlocked, runID)
	}

	resp, err := m.Git.Merge(ctx, &gitv1.MergeRequest{
		Repo:              run.RepoID.String(),
		SourceRef:         run.SourceRef,
		ExpectedSourceSha: head,
		ExpectedTargetSha: target,
		TargetRef:         run.TargetRef,
		Method:            method,
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
func (s *Store) hasIndependentApproval(ctx context.Context, runID, authorID uuid.UUID, head string) (bool, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return false, err
	}
	var count int
	err = s.pool.QueryRow(ctx, `
		SELECT count(*) FROM reviews.run_reviews v JOIN reviews.runs r ON r.id = v.run_id
		WHERE v.run_id = $1 AND v.reviewer_id <> $2 AND v.verdict = 'approve' AND v.source_sha = $3 AND v.source_sha <> '' AND r.org_id = $4`,
		runID, authorID, head, run.OrgID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check independent approval for run %s: %w", runID, err)
	}
	return count > 0, nil
}

// setRunState transitions runID to state (e.g. "merged").
func (s *Store) setRunState(ctx context.Context, runID uuid.UUID, state string) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return fmt.Errorf("set run state requires an organization scope")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE reviews.runs SET state = $1 WHERE id = $2 AND org_id = $3`, state, runID, scope.OrgID)
	if err != nil {
		return fmt.Errorf("set run %s state to %q: %w", runID, state, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("run not found in this organization")
	}
	return nil
}
