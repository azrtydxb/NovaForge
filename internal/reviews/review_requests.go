package reviews

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type reviewRequest struct {
	ID, OrgID, RunID, RequestedBy       uuid.UUID
	SourceSHA, TargetSHA, State, Detail string
	Attempts                            []*reviewsv1.AgentReviewAttempt
	LeaseID                             uuid.UUID
	LeaseUntil, CreatedAt               time.Time
}

const requestColumns = `id,org_id,run_id,requested_by,source_sha,target_sha,state,detail,attempts,COALESCE(lease_id,'00000000-0000-0000-0000-000000000000'),COALESCE(lease_until,created_at),created_at`

func scanReviewRequest(row pgx.Row) (reviewRequest, error) {
	var r reviewRequest
	var raw []byte
	err := row.Scan(&r.ID, &r.OrgID, &r.RunID, &r.RequestedBy, &r.SourceSHA, &r.TargetSHA, &r.State, &r.Detail, &raw, &r.LeaseID, &r.LeaseUntil, &r.CreatedAt)
	if err == nil {
		err = json.Unmarshal(raw, &r.Attempts)
	}
	return r, err
}
func (r reviewRequest) proto() *reviewsv1.AgentReviewRequest {
	return &reviewsv1.AgentReviewRequest{Id: r.ID.String(), RunId: r.RunID.String(), RequestedBy: r.RequestedBy.String(), SourceSha: r.SourceSHA, TargetSha: r.TargetSHA, State: r.State, Detail: r.Detail, CreatedAt: r.CreatedAt.Format(rfc3339), Attempts: r.Attempts}
}

func (g *GRPCServer) RequestAgentReview(ctx context.Context, req *reviewsv1.RequestAgentReviewRequest) (*reviewsv1.RequestAgentReviewResponse, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || scope.ActorKind != "user" || scope.ActorID == uuid.Nil {
		return nil, status.Error(codes.PermissionDenied, "a human member must request independent review")
	}
	if g.ReviewWorker == nil || g.ReviewWorker.Gateway == nil || g.ReviewWorker.Gateway.model == nil || g.ReviewWorker.Gateway.store != g.Store {
		return nil, status.Error(codes.Unavailable, "independent agent review is not configured")
	}
	if err := g.ReviewWorker.Config.Validate(); err != nil {
		return nil, status.Error(codes.Unavailable, "independent agent review limits are invalid")
	}
	run, err := g.resolveRun(ctx, req.GetRunId(), "", 0)
	if err != nil {
		return nil, err
	}
	if run.State != "open" {
		return nil, status.Error(codes.FailedPrecondition, "run is not open")
	}
	head, err := sourceHead(ctx, g.Git, run)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "source revision unavailable")
	}
	if req.GetExpectedSourceSha() == "" || req.GetExpectedSourceSha() != head {
		return nil, status.Error(codes.Aborted, "source head changed; inspect the current revision")
	}
	target, err := refHead(ctx, g.Git, run.RepoID.String(), run.TargetRef)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "target revision unavailable")
	}
	// Unique revision identity deduplicates retries even after failure. No HTTP
	// cancellation, process restart or second requester can repeat model spend.
	r, err := scanReviewRequest(g.Store.pool.QueryRow(ctx, `INSERT INTO reviews.agent_review_requests(id,org_id,run_id,requested_by,source_sha,target_sha,state)
 SELECT $1,$2,id,$4,$5,$6,'queued' FROM reviews.runs WHERE id=$3 AND org_id=$2 AND state='open'
 ON CONFLICT(run_id,source_sha,target_sha) DO UPDATE SET source_sha=EXCLUDED.source_sha
 RETURNING `+requestColumns, uuid.New(), scope.OrgID, run.ID, scope.ActorID, head, target))
	if err != nil {
		return nil, status.Error(codes.Internal, "persist agent review request failed")
	}
	return &reviewsv1.RequestAgentReviewResponse{Request: r.proto()}, nil
}
func (g *GRPCServer) ListAgentReviewRequests(ctx context.Context, req *reviewsv1.ListAgentReviewRequestsRequest) (*reviewsv1.ListAgentReviewRequestsResponse, error) {
	run, err := g.resolveRun(ctx, req.GetRunId(), "", 0)
	if err != nil {
		return nil, err
	}
	rows, err := g.Store.pool.Query(ctx, `SELECT `+requestColumns+` FROM reviews.agent_review_requests WHERE org_id=$1 AND run_id=$2 ORDER BY created_at DESC`, run.OrgID, run.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, "read agent review requests failed")
	}
	defer rows.Close()
	out := &reviewsv1.ListAgentReviewRequestsResponse{Available: g.ReviewWorker != nil && g.ReviewWorker.Gateway != nil && g.ReviewWorker.Gateway.model != nil && g.ReviewWorker.Gateway.store == g.Store, Requests: []*reviewsv1.AgentReviewRequest{}}
	for rows.Next() {
		r, e := scanReviewRequest(rows)
		if e != nil {
			return nil, status.Error(codes.Internal, "read agent review request failed")
		}
		out.Requests = append(out.Requests, r.proto())
	}
	if err = rows.Err(); err != nil {
		return nil, status.Error(codes.Internal, "read agent review requests failed")
	}
	return out, nil
}

// reviewOrganizations is a platform-worker discovery query returning ONLY IDs.
// The worker re-enters an authenticated organization before reading any work.
func (s *Store) reviewOrganizations(ctx context.Context, after uuid.UUID) ([]uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || !scope.IsPlatformWorker() {
		return nil, fmt.Errorf("platform worker required")
	}
	rows, err := s.pool.Query(ctx, `SELECT org_id FROM (
 SELECT org_id FROM reviews.agent_review_requests WHERE state IN ('queued','running')
 UNION SELECT org_id FROM reviews.review_executions WHERE terminated_at IS NULL
 ) organizations ORDER BY (org_id <= $1),org_id LIMIT 8`, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *Store) claimReview(ctx context.Context, c ReviewConfig) (reviewRequest, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return reviewRequest{}, fmt.Errorf("org scope required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return reviewRequest{}, err
	}
	defer tx.Rollback(ctx)
	// Organization lock serializes admission and concurrency across replicas.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "agent-review:"+scope.OrgID.String()); err != nil {
		return reviewRequest{}, err
	}
	// Lease expiry is uncertainty, NOT proof another process died. Never rerun.
	if _, err = tx.Exec(ctx, `UPDATE reviews.agent_review_requests SET state='uncertain',detail='execution lease expired; model outcome may be unknown; not replayed' WHERE org_id=$1 AND state='running' AND lease_until<=now()`, scope.OrgID); err != nil {
		return reviewRequest{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT id AS request_id FROM reviews.agent_review_requests WHERE org_id=$1 AND state='running' UNION SELECT request_id FROM reviews.review_executions WHERE org_id=$1 AND terminated_at IS NULL) outstanding`, scope.OrgID).Scan(&count); err != nil {
		return reviewRequest{}, err
	}
	if count >= c.MaxConcurrentRequests {
		if err = tx.Commit(ctx); err != nil {
			return reviewRequest{}, err
		}
		return reviewRequest{}, pgx.ErrNoRows
	}
	r, err := scanReviewRequest(tx.QueryRow(ctx, `UPDATE reviews.agent_review_requests SET state='running',lease_id=$2,lease_until=now()+$3::interval WHERE id=(SELECT id FROM reviews.agent_review_requests WHERE org_id=$1 AND state='queued' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED) AND org_id=$1 RETURNING `+requestColumns, scope.OrgID, uuid.New(), fmt.Sprintf("%d seconds", c.WallclockSeconds+30)))
	if err == pgx.ErrNoRows {
		if e := tx.Commit(ctx); e != nil {
			return r, e
		}
		return r, err
	}
	if err != nil {
		return r, err
	}
	return r, tx.Commit(ctx)
}

// saveReview atomically records each attempt and its eligible review. A stale
// lease, deleted run or closed run cannot write an approval after the fact.
func (s *Store) saveReview(ctx context.Context, r reviewRequest, attempt *reviewsv1.AgentReviewAttempt) error {
	if err := authz.RequireOrg(ctx, r.OrgID); err != nil {
		return err
	}
	raw, err := json.Marshal(r.Attempts)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE reviews.agent_review_requests SET attempts=$4,state=$5,detail=$6 WHERE id=$1 AND org_id=$2 AND lease_id=$3 AND state='running' AND lease_until>now()`, r.ID, r.OrgID, r.LeaseID, raw, r.State, r.Detail)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("review lease no longer current")
	}
	if attempt != nil && attempt.State == "succeeded" {
		tag, err = tx.Exec(ctx, `INSERT INTO reviews.run_reviews(run_id,reviewer_id,reviewer_kind,verdict,source_sha,summary)
   SELECT id,$3,'agent',$4,$5,$6 FROM reviews.runs WHERE id=$1 AND org_id=$2 AND state='open' AND author_id<>$3
   ON CONFLICT(run_id,reviewer_id) DO UPDATE SET verdict=EXCLUDED.verdict,source_sha=EXCLUDED.source_sha,summary=EXCLUDED.summary,reviewer_kind='agent',created_at=now()`, r.RunID, r.OrgID, attempt.AgentId, attempt.Verdict, r.SourceSHA, attempt.Summary)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("run closed, deleted or self review refused")
		}
	}
	return tx.Commit(ctx)
}
