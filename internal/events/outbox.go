package events

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/redis/go-redis/v9"
)

const StreamEngineeringRuns = "stream:reviews:transitions"
const StreamCIResults = "stream:ci:results"

// RepositoryEvent contains observable lifecycle metadata, not code, logs or
// credentials. EventID survives an XADD acknowledgement lost during a restart.
type RepositoryEvent struct {
	Version       int       `json:"version"`
	EventID       uuid.UUID `json:"event_id"`
	Event         string    `json:"event"`
	OrgID         uuid.UUID `json:"org_id"`
	RepoID        uuid.UUID `json:"repo_id"`
	RunID         uuid.UUID `json:"run_id"`
	State         string    `json:"state"`
	PreviousState string    `json:"previous_state"`
	At            time.Time `json:"at"`
}

// Outbox runs inside the service owning Schema; it never reads another service's
// tables. The schema choice is a closed operator/program constant, not SQL input
// from a request. Database triggers enqueue intent in the state transaction so
// every mutation path, including recovery workers, has the same guarantee.
type Outbox struct {
	Pool   *pgxpool.Pool
	Redis  *redis.Client
	Schema string
	// Stream overrides the production stream for isolated integration tests.
	Stream string
}

func (o Outbox) stream() (string, error) {
	switch o.Schema {
	case "reviews":
		if o.Stream != "" {
			return o.Stream, nil
		}
		return StreamEngineeringRuns, nil
	case "ci":
		if o.Stream != "" {
			return o.Stream, nil
		}
		return StreamCIResults, nil
	default:
		return "", fmt.Errorf("unsupported event outbox owner")
	}
}

// RelayOne inventories opaque IDs under platform authority, then re-enters the
// owner's org scope to load and acknowledge its payload. A row lock admits only
// one replica. Redis is bounded by the caller deadline; failed publication leaves
// the row durable. A crash after XADD may repeat the same EventID.
func (o Outbox) RelayOne(ctx context.Context) (bool, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || !scope.IsPlatformWorker() {
		return false, fmt.Errorf("event relay requires platform authority")
	}
	stream, err := o.stream()
	if err != nil {
		return false, err
	}
	if o.Pool == nil || o.Redis == nil {
		return false, fmt.Errorf("event relay requires database and Redis")
	}
	tx, err := o.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var id, org uuid.UUID
	err = tx.QueryRow(ctx, "SELECT id,org_id FROM "+o.Schema+".event_outbox ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED").Scan(&id, &org)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ctx = authz.WithScope(ctx, authz.Scope{OrgID: org, ActorKind: "service", ServiceName: o.Schema})
	if err := authz.RequireOrg(ctx, org); err != nil {
		return false, err
	}
	var payload []byte
	if err = tx.QueryRow(ctx, "SELECT payload FROM "+o.Schema+".event_outbox WHERE id=$1 AND org_id=$2", id, org).Scan(&payload); err != nil {
		return false, err
	}
	if err = o.Redis.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]any{"data": string(payload)}}).Err(); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM "+o.Schema+".event_outbox WHERE id=$1 AND org_id=$2", id, org); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (o Outbox) Run(ctx context.Context) {
	ctx = authz.WithScope(ctx, authz.Scope{ActorKind: "service", PlatformWorker: o.Schema + "-events"})
	for ctx.Err() == nil {
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		more, err := o.RelayOne(call)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Printf("%s event relay: %v", o.Schema, err)
		}
		if more && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
