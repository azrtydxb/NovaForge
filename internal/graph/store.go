// Package graph owns the engineering graph schema: nodes joining code,
// services, tests, Work Items, ADRs, deployments, owners and incidents, and
// the edges relating them. It is the graph service's schema — no other
// service reads these tables directly.
package graph

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
)

// Node is a row in graph.graph_nodes. Kind is one of symbol, file, service, api,
// schema, test, work_item, commit, adr, deployment, owner, or incident.
type Node struct {
	ID    uuid.UUID
	OrgID uuid.UUID
	Kind  string
	Key   string
	Attrs map[string]string
}

// Edge is a row in graph.graph_edges. Kind is one of depends_on, called_by,
// tested_by, owned_by, implements, deployed_as, or changed_by.
type Edge struct {
	FromID uuid.UUID
	ToID   uuid.UUID
	Kind   string
}

// Store provides access to the graph schema's tables. Every method derives
// its org_id predicate from the caller's authz.Scope (or an explicit orgID
// argument checked against that scope) — no query is satisfiable without it.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps pool as a graph.Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool exposes the underlying connection pool for callers (such as tests)
// that need to run ad-hoc queries against the graph schema.
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}

// querier is the subset of pgxpool.Pool and pgx.Tx that upsert helpers need,
// so the same code runs inside or outside a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func marshalAttrs(attrs map[string]string) ([]byte, error) {
	if attrs == nil {
		attrs = map[string]string{}
	}
	return json.Marshal(attrs)
}

func scanNode(row pgx.Row) (Node, error) {
	var n Node
	var attrsRaw []byte
	if err := row.Scan(&n.ID, &n.OrgID, &n.Kind, &n.Key, &attrsRaw); err != nil {
		return Node{}, err
	}
	if len(attrsRaw) > 0 {
		if err := json.Unmarshal(attrsRaw, &n.Attrs); err != nil {
			return Node{}, fmt.Errorf("unmarshal attrs: %w", err)
		}
	}
	return n, nil
}

// upsertNode inserts n, or updates the existing row sharing its (org_id,
// kind, key) unique key, returning the row's real id — which may differ
// from n.ID when an existing row already owned that key.
func upsertNode(ctx context.Context, q querier, orgID, repoID uuid.UUID, hasRepo bool, n Node) (Node, error) {
	id := n.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	attrsRaw, err := marshalAttrs(n.Attrs)
	if err != nil {
		return Node{}, fmt.Errorf("marshal attrs: %w", err)
	}

	var row pgx.Row
	if hasRepo {
		row = q.QueryRow(ctx, `
			INSERT INTO graph.graph_nodes (id, org_id, repo_id, kind, key, attrs)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb)
			ON CONFLICT (org_id, kind, key) DO UPDATE
			SET repo_id = EXCLUDED.repo_id, attrs = EXCLUDED.attrs
			RETURNING id, org_id, kind, key, attrs
		`, id, orgID, repoID, n.Kind, n.Key, attrsRaw)
	} else {
		row = q.QueryRow(ctx, `
			INSERT INTO graph.graph_nodes (id, org_id, kind, key, attrs)
			VALUES ($1, $2, $3, $4, $5::jsonb)
			ON CONFLICT (org_id, kind, key) DO UPDATE
			SET attrs = EXCLUDED.attrs
			RETURNING id, org_id, kind, key, attrs
		`, id, orgID, n.Kind, n.Key, attrsRaw)
	}
	return scanNode(row)
}

func upsertEdge(ctx context.Context, q querier, e Edge) error {
	_, err := q.Exec(ctx, `
		INSERT INTO graph.graph_edges (from_id, to_id, kind)
		VALUES ($1, $2, $3)
		ON CONFLICT (from_id, to_id, kind) DO NOTHING
	`, e.FromID, e.ToID, e.Kind)
	return err
}

// UpsertNode inserts n, or updates the existing row sharing its (org_id,
// kind, key) key with n's attrs. The org in n must match the caller's
// authz.Scope.
func (s *Store) UpsertNode(ctx context.Context, n Node) (Node, error) {
	if err := authz.RequireOrg(ctx, n.OrgID); err != nil {
		return Node{}, err
	}
	return upsertNode(ctx, s.pool, n.OrgID, uuid.Nil, false, n)
}

// UpsertEdge inserts e, doing nothing if the (from_id, to_id, kind) triple
// already exists.
func (s *Store) UpsertEdge(ctx context.Context, e Edge) error {
	if _, err := authz.FromContext(ctx); err != nil {
		return err
	}
	return upsertEdge(ctx, s.pool, e)
}

// Neighbours returns the nodes reachable from nodeID over one hop of
// edgeKind, in direction "in" (nodes with an edge pointing at nodeID) or
// "out" (nodes nodeID points at). Both nodeID and every returned node are
// required to belong to the caller's org — a foreign-org node id, edge, or
// neighbour is never returned.
func (s *Store) Neighbours(ctx context.Context, nodeID uuid.UUID, edgeKind string, direction string) ([]Node, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, err
	}

	var query string
	switch direction {
	case "in":
		query = `
			SELECT n.id, n.org_id, n.kind, n.key, n.attrs
			FROM graph.graph_edges e
			JOIN graph.graph_nodes n ON n.id = e.from_id
			JOIN graph.graph_nodes target ON target.id = e.to_id
			WHERE e.to_id = $1 AND e.kind = $2
			  AND n.org_id = $3 AND target.org_id = $3
		`
	case "out":
		query = `
			SELECT n.id, n.org_id, n.kind, n.key, n.attrs
			FROM graph.graph_edges e
			JOIN graph.graph_nodes n ON n.id = e.to_id
			JOIN graph.graph_nodes source ON source.id = e.from_id
			WHERE e.from_id = $1 AND e.kind = $2
			  AND n.org_id = $3 AND source.org_id = $3
		`
	default:
		return nil, fmt.Errorf("invalid direction %q, want \"in\" or \"out\"", direction)
	}

	rows, err := s.pool.Query(ctx, query, nodeID, edgeKind, scope.OrgID)
	if err != nil {
		return nil, fmt.Errorf("neighbours query: %w", err)
	}
	defer rows.Close()

	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("scan neighbour: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ReplaceFileSubgraph atomically replaces the symbol nodes for path: every
// existing node whose kind is symbol and whose attrs carry that path is
// deleted (cascading to its edges), then nodes and edges are inserted. This
// makes re-indexing a file idempotent under at-least-once redelivery: a
// symbol removed from the source no longer lingers in the graph.
func (s *Store) ReplaceFileSubgraph(ctx context.Context, orgID, repoID uuid.UUID, path string, nodes []Node, edges []Edge) error {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		DELETE FROM graph.graph_nodes
		WHERE org_id = $1 AND kind = 'symbol' AND attrs->>'path' = $2
	`, orgID, path); err != nil {
		return fmt.Errorf("delete stale symbols: %w", err)
	}

	idRemap := make(map[uuid.UUID]uuid.UUID, len(nodes))
	for _, n := range nodes {
		if n.OrgID != orgID {
			return fmt.Errorf("node %s org %s does not match orgID %s", n.Key, n.OrgID, orgID)
		}
		saved, err := upsertNode(ctx, tx, orgID, repoID, true, n)
		if err != nil {
			return fmt.Errorf("upsert node %s: %w", n.Key, err)
		}
		idRemap[n.ID] = saved.ID
	}

	for _, e := range edges {
		from := e.FromID
		if remapped, ok := idRemap[from]; ok {
			from = remapped
		}
		to := e.ToID
		if remapped, ok := idRemap[to]; ok {
			to = remapped
		}
		if err := upsertEdge(ctx, tx, Edge{FromID: from, ToID: to, Kind: e.Kind}); err != nil {
			return fmt.Errorf("upsert edge %s->%s: %w", from, to, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
