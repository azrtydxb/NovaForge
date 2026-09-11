package agents

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
)

// Entry is a row in the agents.tool_calls table: one tool invocation made by
// an agent run.
type Entry struct {
	ID        uuid.UUID
	RunID     uuid.UUID
	Tool      string
	ArgsJSON  []byte
	Outcome   string
	Error     string
	StartedAt time.Time
	EndedAt   *time.Time
}

// AuditLog is an append-only record of every tool call an agent run makes.
// It exposes only Record and Complete: there is no delete path, so the log
// cannot be edited or erased from the application side.
type AuditLog struct {
	pool *pgxpool.Pool
}

// NewAuditLog wraps pool as an agents.AuditLog.
func NewAuditLog(pool *pgxpool.Pool) *AuditLog {
	return &AuditLog{pool: pool}
}

// Record inserts a pending tool-call entry before the tool executes, so the
// audit log captures every attempted call even if the process crashes before
// Complete is reached.
func (a *AuditLog) Record(ctx context.Context, e Entry) (uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	id := uuid.New()
	_, err = a.pool.Exec(ctx,
		`INSERT INTO agents.tool_calls (id, run_id, org_id, tool, args_json, outcome)
		 VALUES ($1, $2, $3, $4, $5, 'pending')`,
		id, e.RunID, scope.OrgID, e.Tool, e.ArgsJSON,
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("record tool call: %w", err)
	}
	return id, nil
}

// Complete marks a previously recorded tool call as finished with outcome
// (e.g. "ok" or "error") and, for errors, errText.
func (a *AuditLog) Complete(ctx context.Context, id uuid.UUID, outcome string, errText string) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	tag, err := a.pool.Exec(ctx,
		`UPDATE agents.tool_calls SET outcome = $1, error = $2, ended_at = now()
		 WHERE id = $3 AND org_id = $4`,
		outcome, errText, id, scope.OrgID,
	)
	if err != nil {
		return fmt.Errorf("complete tool call: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("tool call %s not found", id)
	}
	return nil
}

// List returns every tool call recorded for runID, oldest first, scoped to
// the caller's org.
func (a *AuditLog) List(ctx context.Context, runID uuid.UUID) ([]Entry, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := a.pool.Query(ctx,
		`SELECT id, run_id, tool, args_json, outcome, error, started_at, ended_at
		 FROM agents.tool_calls WHERE run_id = $1 AND org_id = $2 ORDER BY started_at`,
		runID, scope.OrgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list tool calls: %w", err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.RunID, &e.Tool, &e.ArgsJSON, &e.Outcome, &e.Error, &e.StartedAt, &e.EndedAt); err != nil {
			return nil, fmt.Errorf("scan tool call: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tool calls: %w", err)
	}
	return out, nil
}
