package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
)

// Entry is a row in the agents.tool_calls table: one tool invocation made by
// an agent run.
type Entry struct {
	ID       uuid.UUID
	RunID    uuid.UUID
	Tool     string
	ArgsJSON []byte
	// ArgsAudited marks ArgsJSON as already reduced to the audited
	// representation, so Record keeps it instead of replacing it.
	ArgsAudited bool
	Outcome     string
	Error       string
	StartedAt   time.Time
	EndedAt     *time.Time
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
	id := e.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	// Only trusted run evidence has a separate schema. A caller that has already
	// reduced a tool call's arguments to the audited representation says so
	// (tools.AuditArgs decides, per tool, which arguments are identifiers kept
	// verbatim and which are content kept as a digest). Anything else is reduced
	// here, so an unconsidered caller cannot make the audit log a copy of
	// whatever it was passed.
	if e.Tool != "run.summary" && e.Tool != "run.verification" && !e.ArgsAudited {
		e.ArgsJSON, _ = json.Marshal(map[string]any{"argument_bytes": len(e.ArgsJSON), "valid_json": json.Valid(e.ArgsJSON)})
	}
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)
	var parent uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM agents.agent_runs WHERE id=$1 AND org_id=$2 FOR UPDATE`, e.RunID, scope.OrgID).Scan(&parent); err != nil {
		return uuid.Nil, fmt.Errorf("audit parent unavailable: %w", err)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agents.tool_calls(id,run_id,org_id,tool,args_json,outcome) VALUES($1,$2,$3,$4,$5,'pending')
 ON CONFLICT(id) DO UPDATE SET id=EXCLUDED.id WHERE tool_calls.run_id=EXCLUDED.run_id AND tool_calls.org_id=EXCLUDED.org_id AND tool_calls.tool=EXCLUDED.tool AND tool_calls.args_json=EXCLUDED.args_json`, id, e.RunID, scope.OrgID, e.Tool, e.ArgsJSON)
	if err != nil {
		return uuid.Nil, err
	}
	if tag.RowsAffected() != 1 {
		return uuid.Nil, fmt.Errorf("audit identity conflict")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agents.tool_events(org_id,run_id,call_id,tool,outcome) VALUES($1,$2,$3,$4,'pending') ON CONFLICT DO NOTHING`, scope.OrgID, e.RunID, id, e.Tool); err != nil {
		return uuid.Nil, err
	}
	return id, tx.Commit(ctx)
}

// Complete atomically stores an immutable terminal receipt and its outbox event.
// Unknown provider text is deliberately omitted, not guessed to be secret-free.
func (a *AuditLog) Complete(ctx context.Context, id uuid.UUID, outcome string, _ string) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	switch outcome {
	case "ok", "error", "denied", "refused", "cancelled", "unavailable":
	default:
		return fmt.Errorf("invalid terminal tool outcome")
	}
	errText := ""
	if outcome != "ok" {
		errText = "tool " + outcome
	}
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var run uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT r.id FROM agents.agent_runs r JOIN agents.tool_calls c ON c.run_id=r.id AND c.org_id=r.org_id WHERE c.id=$1 AND r.org_id=$2 FOR UPDATE OF r`, id, scope.OrgID).Scan(&run); err != nil {
		return err
	}
	var tool string
	err = tx.QueryRow(ctx, `UPDATE agents.tool_calls SET outcome=$1,error=$2,ended_at=COALESCE(ended_at,clock_timestamp()) WHERE id=$3 AND org_id=$4 AND ((outcome='pending' AND ended_at IS NULL) OR (outcome=$1 AND error=$2 AND ended_at IS NOT NULL)) RETURNING tool`, outcome, errText, id, scope.OrgID).Scan(&tool)
	if err != nil {
		return fmt.Errorf("terminal audit receipt conflicts or unavailable: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agents.tool_events(org_id,run_id,call_id,tool,outcome) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, scope.OrgID, run, id, tool, outcome); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
