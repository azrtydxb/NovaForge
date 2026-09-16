package graph

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
)

// UnreferencedSymbols returns candidates, not proof that code can be deleted.
// The indexer's depends_on edges include callers and type references; checking
// only called_by (which it does not produce) marked referenced code unused.
// These queries live with their schema rather than in maintenance's service.
func (s *Store) UnreferencedSymbols(ctx context.Context, orgID, repoID uuid.UUID) ([]Node, error) {
	if err := maintenanceScope(ctx, orgID, repoID); err != nil {
		return nil, err
	}
	return s.queryNodes(ctx, `
  SELECT n.id,n.org_id,n.kind,n.key,n.attrs
  FROM graph.graph_nodes n
  WHERE n.org_id=$1 AND n.repo_id=$2 AND n.kind='symbol'
   AND NOT EXISTS (
    SELECT 1 FROM graph.graph_edges e
    JOIN graph.graph_nodes caller ON caller.id=e.from_id AND caller.org_id=$1 AND caller.repo_id=$2
    WHERE e.to_id=n.id AND e.kind IN ('depends_on','called_by')
   )
   AND NOT EXISTS (
    SELECT 1 FROM graph.graph_edges e
    JOIN graph.graph_nodes test ON test.id=e.to_id AND test.org_id=$1 AND test.repo_id=$2
    WHERE e.from_id=n.id AND e.kind='tested_by'
   )
  ORDER BY n.key`, orgID, repoID)
}

// MissingSymbols checks exact graph keys within this repository. A symbol in
// a different repository cannot establish that this document remains valid.
func (s *Store) MissingSymbols(ctx context.Context, orgID, repoID uuid.UUID, keys []string) ([]string, error) {
	if err := maintenanceScope(ctx, orgID, repoID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
  SELECT DISTINCT key FROM unnest($3::text[]) AS requested(key)
  WHERE NOT EXISTS (
   SELECT 1 FROM graph.graph_nodes n
   WHERE n.org_id=$1 AND n.repo_id=$2 AND n.kind='symbol' AND n.key=requested.key
  ) ORDER BY key`, orgID, repoID, keys)
	if err != nil {
		return nil, fmt.Errorf("check symbols: %w", err)
	}
	defer rows.Close()
	var missing []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		missing = append(missing, key)
	}
	return missing, rows.Err()
}

func maintenanceScope(ctx context.Context, orgID, repoID uuid.UUID) error {
	if orgID == uuid.Nil || repoID == uuid.Nil {
		return fmt.Errorf("graph maintenance requires organization and repository")
	}
	return authz.RequireOrg(ctx, orgID)
}
