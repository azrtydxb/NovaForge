package deployment

import (
	"context"
	"errors"
	"log"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/redis/go-redis/v9"
)

const StreamDeploymentSucceeded = "stream:deployment:succeeded"

// SuccessEvent attests observed delivery only. Artifact-to-commit lineage requires
// separate owner attestation and must not be inferred from RunID. Consumers dedupe
// EvidenceID: a crash after XADD but before acknowledgement can repeat this event.
type SuccessEvent struct {
	EvidenceID           string    `json:"evidence_id"`
	OrgID                uuid.UUID `json:"org_id"`
	RepoID               uuid.UUID `json:"repo_id"`
	OperationID          uuid.UUID `json:"operation_id"`
	RunID                uuid.UUID `json:"run_id"`
	Artifact             string    `json:"artifact"`
	Target               string    `json:"target"`
	Destination          string    `json:"destination"`
	TargetRevision       string    `json:"target_revision"`
	LatestExecuteAttempt int       `json:"latest_execute_attempt"`
	ExternalID           string    `json:"external_id"`
}

func enqueueSuccess(ctx context.Context, tx pgx.Tx, op Operation, number int, result Result) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || scope.OrgID != op.OrgID || result.ExternalID == "" {
		return ErrUncertain
	}
	var attempt int
	err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(number),0) FROM deployment.attempts WHERE org_id=$1 AND operation_id=$2 AND kind='execute' AND number<=$3`, scope.OrgID, op.ID, number).Scan(&attempt)
	if err != nil {
		return err
	}
	if attempt == 0 {
		return ErrUncertain
	}
	event := SuccessEvent{EvidenceID: op.ID.String() + ":" + strconv.Itoa(attempt), OrgID: scope.OrgID, RepoID: op.RepoID, OperationID: op.ID, RunID: op.RunID, Artifact: op.Artifact, Target: op.Target, Destination: op.Destination, TargetRevision: op.TargetRevision, LatestExecuteAttempt: attempt, ExternalID: result.ExternalID}
	_, err = tx.Exec(ctx, `INSERT INTO deployment.success_outbox(org_id,operation_id,execute_attempt,payload) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, scope.OrgID, op.ID, attempt, event)
	return err
}

type successIdentity struct {
	org, operation uuid.UUID
	attempt        int
}

// claimSuccess is the named platform-worker inventory exception. It returns only
// opaque identities; payload access and acknowledgement re-enter each org scope.
// Rotating attempted timestamps prevents one unavailable item starving the queue.
func (s *Service) claimSuccess(ctx context.Context, limit int) ([]successIdentity, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || !scope.IsPlatformWorker() || scope.PlatformWorker != "deployment-events" {
		return nil, errors.New("deployment event discovery requires named platform worker")
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("invalid event batch limit")
	}
	rows, err := s.pool.Query(ctx, `WITH selected AS (
 SELECT org_id,operation_id,execute_attempt FROM deployment.success_outbox WHERE published_at IS NULL
 ORDER BY publish_attempted_at ASC NULLS FIRST,org_id,operation_id,execute_attempt LIMIT $1 FOR UPDATE SKIP LOCKED)
 UPDATE deployment.success_outbox o SET publish_attempted_at=clock_timestamp() FROM selected s
 WHERE o.org_id=s.org_id AND o.operation_id=s.operation_id AND o.execute_attempt=s.execute_attempt
 RETURNING o.org_id,o.operation_id,o.execute_attempt`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []successIdentity
	for rows.Next() {
		var id successIdentity
		if err := rows.Scan(&id.org, &id.operation, &id.attempt); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Service) publishSuccess(ctx context.Context, id successIdentity, publish func(context.Context, SuccessEvent) error) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || scope.OrgID != id.org || scope.ActorKind != "service" || scope.ActorID != uuid.Nil || scope.ServiceName != "deployment-events" {
		return errors.New("deployment publication requires organization event service")
	}
	var event SuccessEvent
	err = s.pool.QueryRow(ctx, `SELECT payload FROM deployment.success_outbox WHERE org_id=$1 AND operation_id=$2 AND execute_attempt=$3 AND published_at IS NULL`, scope.OrgID, id.operation, id.attempt).Scan(&event)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = publish(ctx, event); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE deployment.success_outbox SET published_at=clock_timestamp() WHERE org_id=$1 AND operation_id=$2 AND execute_attempt=$3 AND published_at IS NULL`, scope.OrgID, id.operation, id.attempt)
	return err
}

// PublishPendingSuccess retries durable delivery events, including after author
// deletion. It is internal service composition, not a public operation or tool.
func (s *Service) PublishPendingSuccess(ctx context.Context, hmacSecret string, rdb *redis.Client, limit int) error {
	ids, err := s.claimSuccess(ctx, limit)
	if err != nil {
		return err
	}
	if rdb == nil {
		return errors.New("deployment publisher requires Redis")
	}
	var failures []error
	for _, id := range ids {
		token, err := svcauth.Mint(hmacSecret, "deployment-events", id.org, time.Minute)
		if err != nil {
			return err
		}
		scope, err := svcauth.ScopeFromToken(hmacSecret, token)
		if err != nil {
			return err
		}
		call, cancel := context.WithTimeout(authz.WithScope(ctx, scope), 10*time.Second)
		err = s.publishSuccess(call, id, func(ctx context.Context, event SuccessEvent) error {
			return events.Publish(ctx, rdb, StreamDeploymentSucceeded, event)
		})
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
	}
	return errors.Join(failures...)
}

// RunSuccessPublisher blocks until shutdown. Startup must launch it alongside
// RunCleanup; recording an outbox without this caller does not publish events.
func (s *Service) RunSuccessPublisher(ctx context.Context, hmacSecret string, rdb *redis.Client) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		token, err := svcauth.MintPlatform(hmacSecret, "deployment-events", time.Minute)
		if err == nil {
			var name string
			name, err = svcauth.VerifyPlatform(hmacSecret, token)
			if err == nil {
				err = s.PublishPendingSuccess(authz.WithScope(ctx, authz.Scope{ActorKind: "service", PlatformWorker: name}), hmacSecret, rdb, 25)
			}
		}
		if err != nil && ctx.Err() == nil {
			log.Print("deployment: success publication pending; will retry")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
