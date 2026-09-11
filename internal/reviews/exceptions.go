package reviews

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
)

// Summary counts every open Engineering Run in one organization by the
// single exception category (or "healthy") it currently falls into. The
// six fields correspond one-to-one with the six classify() outcomes below,
// so every open run is counted in exactly one field.
type Summary struct {
	AgentsRunning         int
	ReadyToAutoMerge      int
	NeedHumanReview       int
	ArchitectureDecisions int
	GateFailures          int
	AgentsBlocked         int
}

// Item is one run that needs attention: enough to show it on the exception
// dashboard and explain why it is there.
type Item struct {
	// Key identifies the run for display (its repository-scoped run
	// number, since reviews.Run does not itself carry the originating
	// Work Item's key — that lives in a different schema this package
	// does not read directly).
	Key    string
	Title  string
	State  string
	Reason string
}

// runCategory is one of the six exception categories a run can classify
// into, or categoryHealthy for a run that needs no attention at all.
type runCategory string

const (
	categoryGateFailure          runCategory = "gate_failure"
	categoryArchitectureDecision runCategory = "architecture_decision"
	categoryNeedHumanReview      runCategory = "need_human_review"
	categoryAgentBlocked         runCategory = "agent_blocked"
	categoryAgentRunning         runCategory = "agent_running"
	categoryHealthy              runCategory = "healthy"
)

// architectureDecisionGate and agentBlockedGate are the synthetic proof
// gate names this package's own producers (the swarm scheduler and agent
// runtime, in production wiring) record through Store.RecordProof to
// signal "this run needs an architecture decision" or "this agent run is
// blocked" respectively — proof records already are this package's
// general-purpose per-run evidence log (Task 4 uses the same mechanism
// for review verdicts, under the "review:<role>" gate name prefix), so
// classify() reads them the same way it reads a real gate's proof.
const (
	architectureDecisionGate = "architecture-decision"
	agentBlockedGate         = "agent-blocked"
)

const reviewGatePrefix = "review:"

// classify inspects one run's proof records and review verdicts and
// returns the single category it belongs in, plus a human-readable reason
// (empty for categoryHealthy). Checks run in a fixed priority order so a
// run matching more than one signal still lands in exactly one category —
// what makes Summary's six counters sum to the number of non-healthy open
// runs, no run double-counted.
func classify(run Run, proofs []ProofRecord, reviews []reviewRow) (runCategory, string) {
	for _, p := range proofs {
		if strings.HasPrefix(p.Gate, reviewGatePrefix) || p.Gate == architectureDecisionGate || p.Gate == agentBlockedGate {
			continue
		}
		if p.Status == "fail" || p.Status == "error" {
			return categoryGateFailure, fmt.Sprintf("gate %q %s: %s", p.Gate, p.Status, p.Detail)
		}
	}

	for _, p := range proofs {
		if p.Gate == architectureDecisionGate && p.Status != "pass" {
			return categoryArchitectureDecision, "architecture decision required: " + p.Detail
		}
	}

	for _, r := range reviews {
		if r.verdict == "request_changes" {
			return categoryNeedHumanReview, "a reviewer requested changes"
		}
	}

	if run.AuthorKind == "agent" {
		for _, p := range proofs {
			if p.Gate == agentBlockedGate && p.Status == "fail" {
				return categoryAgentBlocked, "agent run blocked: " + p.Detail
			}
		}
		if len(proofs) == 0 {
			return categoryAgentRunning, "agent run still in progress"
		}
	}

	approved := false
	for _, r := range reviews {
		if r.verdict == "approve" && r.reviewerID != run.AuthorID {
			approved = true
		}
	}
	allNonReviewProofsPass := len(proofs) > 0
	for _, p := range proofs {
		if strings.HasPrefix(p.Gate, reviewGatePrefix) || p.Gate == architectureDecisionGate || p.Gate == agentBlockedGate {
			continue
		}
		if p.Status != "pass" {
			allNonReviewProofsPass = false
		}
	}
	if approved && allNonReviewProofsPass {
		return categoryHealthy, ""
	}

	return categoryNeedHumanReview, "awaiting independent review"
}

// reviewRow is one row of reviews.run_reviews, read directly (there is no
// exported "list reviews" method on Store; SubmitReview and the
// independent-approval check are its only existing read/write paths for
// this table) since exceptions.go lives in the same package as store.go
// and merge.go.
type reviewRow struct {
	reviewerID uuid.UUID
	verdict    string
}

func (s *Store) listReviews(ctx context.Context, runID uuid.UUID) ([]reviewRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT reviewer_id, verdict FROM reviews.run_reviews WHERE run_id = $1`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("list reviews: %w", err)
	}
	defer rows.Close()

	var out []reviewRow
	for rows.Next() {
		var r reviewRow
		if err := rows.Scan(&r.reviewerID, &r.verdict); err != nil {
			return nil, fmt.Errorf("scan review: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list reviews: %w", err)
	}
	return out, nil
}

// openRuns returns every open run for orgID, read with an explicit org_id
// predicate directly against reviews.runs (Store.ListRuns requires a
// specific repoID; the dashboard spans every repository in the org).
func (s *Store) openRuns(ctx context.Context, orgID uuid.UUID) ([]Run, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, repo_id, work_item_id, number, title, source_ref, target_ref,
		       state, author_id, author_kind, agent_name, model_name, created_at
		FROM reviews.runs
		WHERE org_id = $1 AND state = 'open'
		ORDER BY repo_id, number`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("open runs: %w", err)
	}
	defer rows.Close()

	var runs []Run
	for rows.Next() {
		var run Run
		var workItemID *uuid.UUID
		var agentName, modelName *string
		if err := rows.Scan(&run.ID, &run.OrgID, &run.RepoID, &workItemID, &run.Number, &run.Title,
			&run.SourceRef, &run.TargetRef, &run.State, &run.AuthorID, &run.AuthorKind,
			&agentName, &modelName, &run.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		if workItemID != nil {
			run.WorkItemID = *workItemID
		}
		if agentName != nil {
			run.AgentName = *agentName
		}
		if modelName != nil {
			run.ModelName = *modelName
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("open runs: %w", err)
	}
	return runs, nil
}

// Exceptions computes the exception dashboard for orgID: a Summary
// counting every open run by category, and the list of Items that need
// attention (every run NOT classified categoryHealthy). It is built from
// one query per category's raw material — openRuns, then each run's own
// ListProof and listReviews — unioned in Go via classify, so a query
// missing its org_id predicate could never widen the result: openRuns is
// the only query that spans more than one run, and it carries org_id
// itself.
func (s *Store) Exceptions(ctx context.Context, orgID uuid.UUID) (Summary, []Item, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return Summary{}, nil, err
	}

	runs, err := s.openRuns(ctx, orgID)
	if err != nil {
		return Summary{}, nil, fmt.Errorf("exceptions: %w", err)
	}

	var summary Summary
	var items []Item
	for _, run := range runs {
		proofs, err := s.ListProof(ctx, run.ID)
		if err != nil {
			return Summary{}, nil, fmt.Errorf("exceptions: list proof for run %s: %w", run.ID, err)
		}
		revs, err := s.listReviews(ctx, run.ID)
		if err != nil {
			return Summary{}, nil, fmt.Errorf("exceptions: list reviews for run %s: %w", run.ID, err)
		}

		category, reason := classify(run, proofs, revs)
		switch category {
		case categoryGateFailure:
			summary.GateFailures++
		case categoryArchitectureDecision:
			summary.ArchitectureDecisions++
		case categoryNeedHumanReview:
			summary.NeedHumanReview++
		case categoryAgentBlocked:
			summary.AgentsBlocked++
		case categoryAgentRunning:
			summary.AgentsRunning++
		case categoryHealthy:
			summary.ReadyToAutoMerge++
		}

		if category != categoryHealthy {
			items = append(items, Item{
				Key:    fmt.Sprintf("run-%d", run.Number),
				Title:  run.Title,
				State:  run.State,
				Reason: reason,
			})
		}
	}

	return summary, items, nil
}
