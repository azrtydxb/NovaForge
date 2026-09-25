package agents

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// Activity is what one agent is doing now: the run it is executing and the Work
// Item that run is for, or zero values when it holds none. Every agent in the
// organization gets one, including the idle ones — a roster that listed only the
// busy agents could not show that an architect has nothing to do, which is
// exactly what section 24 asks the product to surface.
type Activity struct {
	AgentID     uuid.UUID
	Name        string
	Role        string
	Enabled     bool
	RunID       uuid.UUID
	WorkItemKey string
	Since       time.Time
}

// ListAgentActivity reports every agent in the caller's organization with the run
// it is currently executing.
//
// The Work Item's key is read from the run's own frozen snapshot rather than from
// the Work service's tables: this answer must not depend on reading another
// service's schema. A run whose snapshot has not been frozen yet reports the run
// without a key rather than omitting the agent, because "working, on what is not
// yet recorded" is true and "idle" would not be.
func (s *Store) ListAgentActivity(ctx context.Context) ([]Activity, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	// One running run per agent: the branch lock permits only one at a time, and
	// the earliest is the one that holds it if that ever changes.
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.name, a.role, a.enabled,
		       r.id, r.started_at, r.work_item_snapshot
		FROM agents.agents a
		LEFT JOIN LATERAL (
		    SELECT id, started_at, work_item_snapshot
		    FROM agents.agent_runs
		    WHERE org_id = a.org_id AND agent_id = a.id AND state = 'running'
		    ORDER BY started_at NULLS LAST, id
		    LIMIT 1
		) r ON true
		WHERE a.org_id = $1
		ORDER BY a.name`, scope.OrgID)
	if err != nil {
		return nil, fmt.Errorf("list agent activity: %w", err)
	}
	defer rows.Close()

	out := []Activity{}
	for rows.Next() {
		var a Activity
		var runID *uuid.UUID
		var startedAt *time.Time
		var snapshot []byte
		if err := rows.Scan(&a.AgentID, &a.Name, &a.Role, &a.Enabled, &runID, &startedAt, &snapshot); err != nil {
			return nil, fmt.Errorf("scan agent activity: %w", err)
		}
		if runID != nil {
			a.RunID = *runID
			if startedAt != nil {
				a.Since = *startedAt
			}
			if len(snapshot) > 0 {
				var item workv1.WorkItem
				// An unreadable snapshot must not hide that the agent is busy, so
				// the key is left empty and the run is still reported.
				if err := proto.Unmarshal(snapshot, &item); err == nil {
					a.WorkItemKey = item.GetKey()
				}
			}
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
