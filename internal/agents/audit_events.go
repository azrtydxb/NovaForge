package agents

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/redis/go-redis/v9"
)

// ToolEvent is allowlisted observable metadata; never arguments, results or
// provider error text. Cursor identifies one phase; CallID joins both phases.
type ToolEvent struct {
	Sequence int64     `json:"-"`
	RunID    uuid.UUID `json:"run_id"`
	CallID   uuid.UUID `json:"call_id"`
	Cursor   string    `json:"cursor"`
	Type     string    `json:"type"`
	Tool     string    `json:"tool"`
	Outcome  string    `json:"outcome"`
	At       time.Time `json:"at"`
}

// ErrToolEventCursor denotes invalid input, never a storage availability error.
var ErrToolEventCursor = errors.New("tool event cursor unavailable")

// NewAuditRedisClient isolates notification I/O from the runtime's other Redis
// uses. Socket and context deadlines both apply, including connection setup.
func NewAuditRedisClient(options *redis.Options) *redis.Client {
	opts := *options
	// NewClient initializes notification handlers in Options. Do not reuse the
	// runtime client's mutable processor/maintenance configuration.
	opts.PushNotificationProcessor = nil
	if opts.MaintNotificationsConfig != nil {
		config := *opts.MaintNotificationsConfig
		opts.MaintNotificationsConfig = &config
	}
	opts.ContextTimeoutEnabled = true
	opts.DialTimeout = 200 * time.Millisecond
	opts.ReadTimeout = 200 * time.Millisecond
	opts.WriteTimeout = 200 * time.Millisecond
	opts.PoolTimeout = 200 * time.Millisecond
	opts.MaxRetries = -1
	return redis.NewClient(&opts)
}

// ToolEvents reads authoritative PostgreSQL history, not the lossy notification
// transport. Empty cursor replays all. A cursor must name an existing event in
// this org/run; missing/expired/future cursors fail rather than silently skip.
func (a *AuditLog) ToolEvents(ctx context.Context, run uuid.UUID, cursor string) ([]ToolEvent, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	after := int64(0)
	if cursor != "" {
		if len(cursor) > 64 {
			return nil, ErrToolEventCursor
		}
		parts := strings.Split(cursor, ":")
		if len(parts) != 2 || parts[0] != run.String() {
			return nil, ErrToolEventCursor
		}
		after, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || after <= 0 || strconv.FormatInt(after, 10) != parts[1] {
			return nil, ErrToolEventCursor
		}
		var exists bool
		err = a.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agents.tool_events WHERE org_id=$1 AND run_id=$2 AND sequence=$3)`, scope.OrgID, run, after).Scan(&exists)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrToolEventCursor
		}
	}
	rows, err := a.pool.Query(ctx, `SELECT sequence,call_id,tool,outcome,at FROM agents.tool_events WHERE org_id=$1 AND run_id=$2 AND sequence>$3 ORDER BY sequence LIMIT 100`, scope.OrgID, run, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolEvent
	for rows.Next() {
		e := ToolEvent{RunID: run, Type: "tool_call"}
		if err = rows.Scan(&e.Sequence, &e.CallID, &e.Tool, &e.Outcome, &e.At); err != nil {
			return nil, err
		}
		e.Cursor = run.String() + ":" + strconv.FormatInt(e.Sequence, 10)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ReconcileRun closes only calls whose executing run has durably finished. A
// crash/lost receipt is unavailable, never success or assumed process death.
func (a *AuditLog) ReconcileRun(ctx context.Context, run uuid.UUID) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	rows, err := a.pool.Query(ctx, `SELECT c.id FROM agents.tool_calls c JOIN agents.agent_runs r ON r.id=c.run_id AND r.org_id=c.org_id WHERE c.org_id=$1 AND c.run_id=$2 AND c.outcome='pending' AND r.execution_finished ORDER BY c.started_at,c.id LIMIT 100`, scope.OrgID, run)
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = a.Complete(ctx, id, "unavailable", ""); err != nil {
			return err
		}
	}
	return nil
}

// MaintainAudit is a bounded platform-worker inventory: only org/run IDs leave
// the unscoped scan, then each operation re-enters its organization. Redis is
// at-least-once: a lost acknowledgement may duplicate the stable event cursor.
func (a *AuditLog) MaintainAudit(ctx context.Context, rdb *redis.Client) error {
	rows, err := a.pool.Query(ctx, `SELECT DISTINCT c.org_id,c.run_id FROM agents.tool_calls c JOIN agents.agent_runs r ON r.id=c.run_id AND r.org_id=c.org_id WHERE c.outcome='pending' AND r.execution_finished LIMIT 100`)
	if err != nil {
		return err
	}
	type pair struct{ org, run uuid.UUID }
	var runs []pair
	for rows.Next() {
		var p pair
		if err = rows.Scan(&p.org, &p.run); err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range runs {
		scoped := authz.WithScope(ctx, authz.Scope{OrgID: p.org, ActorKind: "service", ServiceName: "agent-runtime"})
		if err = a.ReconcileRun(scoped, p.run); err != nil {
			return err
		}
	}
	if rdb == nil {
		return nil
	}
	rows, err = a.pool.Query(ctx, `SELECT org_id,run_id FROM agents.tool_events WHERE NOT published GROUP BY org_id,run_id ORDER BY min(sequence) LIMIT 100`)
	if err != nil {
		return err
	}
	runs = nil
	for rows.Next() {
		var p pair
		if err = rows.Scan(&p.org, &p.run); err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range runs {
		if err = a.publishRun(ctx, rdb, p.org, p.run); err != nil {
			return err
		}
	}
	return nil
}
func (a *AuditLog) publishRun(ctx context.Context, rdb *redis.Client, org, run uuid.UUID) error {
	// A publisher-only session lock serializes notifications without locking
	// agent_runs or holding a transaction across network I/O. Audit writers never
	// take this lock. A failed unlock discards the connection (and its lock).
	conn, err := a.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	key := "audit/publisher/" + org.String() + "/" + run.String()
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&locked); err != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
		_ = conn.Hijack().Close(closeCtx)
		cancel()
		return err
	}
	if !locked {
		conn.Release()
		return nil
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
		defer cancel()
		var unlocked bool
		if err := conn.QueryRow(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key).Scan(&unlocked); err != nil || !unlocked {
			_ = conn.Hijack().Close(cleanup)
		} else {
			conn.Release()
		}
	}()

	rows, err := conn.Query(ctx, `SELECT sequence,call_id,tool,outcome,at FROM agents.tool_events WHERE org_id=$1 AND run_id=$2 AND NOT published ORDER BY sequence LIMIT 100`, org, run)
	if err != nil {
		return err
	}
	var batch []ToolEvent
	for rows.Next() {
		e := ToolEvent{RunID: run, Type: "tool_call"}
		if err = rows.Scan(&e.Sequence, &e.CallID, &e.Tool, &e.Outcome, &e.At); err != nil {
			rows.Close()
			return err
		}
		e.Cursor = run.String() + ":" + strconv.FormatInt(e.Sequence, 10)
		batch = append(batch, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range batch {
		publishCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		err = events.Publish(publishCtx, rdb, events.StreamAgentEvents, e)
		cancel()
		if err != nil {
			return err
		}
		// A successful prefix survives a later timeout. If XADD's reply was lost,
		// retry repeats the same cursor; never guess that it was delivered.
		ackCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
		_, err = conn.Exec(ackCtx, `UPDATE agents.tool_events SET published=true WHERE org_id=$1 AND run_id=$2 AND sequence=$3`, org, run, e.Sequence)
		stop()
		if err != nil {
			return err
		}
	}
	return nil
}
