// Package knowledge owns the knowledge service's schema: persistent,
// per-repository project knowledge (decisions, patterns, incidents,
// corrections, and operational notes) that supersedes rather than
// overwrites, so a stale conclusion can never silently reappear in an
// agent's context.
package knowledge

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
)

// EmbeddingDim is the fixed vector width knowledge_entries (and the graph's
// code_chunks) store, matching the vector(1024) columns of their migrations:
// the width of bge-m3, the embedding model the platform's gateway serves. It
// was 768, which no served model produces.
const EmbeddingDim = 1024

const embeddingDim = EmbeddingDim

// Entry is a row in knowledge_entries. Kind is one of decision, pattern,
// incident, correction, or operational.
type Entry struct {
	ID           uuid.UUID
	OrgID        uuid.UUID
	RepoID       uuid.UUID
	Key          string
	Kind         string
	Title        string
	Body         string
	SourceRunID  *uuid.UUID
	SupersededBy *uuid.UUID
	CreatedAt    time.Time
}

// Store provides access to the knowledge schema's tables. Every method
// derives its org_id (and, for Search, repo_id) predicate from the caller's
// authz.Scope or an explicit argument checked against it — no query is
// satisfiable without one.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps pool as a knowledge.Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func scanEntry(row pgx.Row) (Entry, error) {
	var e Entry
	if err := row.Scan(&e.ID, &e.OrgID, &e.RepoID, &e.Key, &e.Kind, &e.Title, &e.Body,
		&e.SourceRunID, &e.SupersededBy, &e.CreatedAt); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Record inserts a new knowledge entry with embedding, or updates the
// existing row sharing its (org_id, repo_id, key) key. The org in e must
// match the caller's authz.Scope.
func (s *Store) Record(ctx context.Context, e Entry, embedding []float32) (Entry, error) {
	if err := authz.RequireOrg(ctx, e.OrgID); err != nil {
		return Entry{}, err
	}
	if len(embedding) != 0 && len(embedding) != embeddingDim {
		return Entry{}, fmt.Errorf("embedding has %d dimensions, want %d", len(embedding), embeddingDim)
	}

	id := e.ID
	if id == uuid.Nil {
		id = uuid.New()
	}

	var embArg any
	if len(embedding) > 0 {
		embArg = vectorLiteral(embedding)
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO knowledge.knowledge_entries
			(id, org_id, repo_id, key, kind, title, body, source_run_id, embedding)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::vector)
		ON CONFLICT (org_id, repo_id, key) DO UPDATE
		SET kind = EXCLUDED.kind, title = EXCLUDED.title, body = EXCLUDED.body,
			source_run_id = EXCLUDED.source_run_id, embedding = EXCLUDED.embedding
		RETURNING id, org_id, repo_id, key, kind, title, body, source_run_id, superseded_by, created_at
	`, id, e.OrgID, e.RepoID, e.Key, e.Kind, e.Title, e.Body, e.SourceRunID, embArg)

	return scanEntry(row)
}

// Get returns the entry with id, regardless of whether it has been
// superseded — a superseded entry stays fetchable by id even though Search
// never returns it. The entry must belong to the caller's org.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (Entry, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return Entry{}, err
	}
	row := s.pool.QueryRow(ctx, `
		SELECT id, org_id, repo_id, key, kind, title, body, source_run_id, superseded_by, created_at
		FROM knowledge.knowledge_entries
		WHERE id = $1 AND org_id = $2
	`, id, scope.OrgID)
	return scanEntry(row)
}

// Search returns the k entries nearest query by cosine distance, nearest
// first, restricted to orgID and repoID, and always excluding superseded
// entries — filtered in the SQL itself, so a superseded decision can never
// reach an agent's context.
func (s *Store) Search(ctx context.Context, orgID, repoID uuid.UUID, query []float32, k int) ([]Entry, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, repo_id, key, kind, title, body, source_run_id, superseded_by, created_at
		FROM knowledge.knowledge_entries
		WHERE org_id = $2 AND repo_id = $3 AND superseded_by IS NULL AND embedding IS NOT NULL
		ORDER BY embedding <=> $1::vector
		LIMIT $4
	`, vectorLiteral(query), orgID, repoID, k)
	if err != nil {
		return nil, fmt.Errorf("search query: %w", err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan entry: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Supersede marks oldID as superseded by newID: it stops appearing in
// Search but remains fetchable by Get. Both entries must belong to the
// caller's org.
func (s *Store) Supersede(ctx context.Context, oldID, newID uuid.UUID) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE knowledge.knowledge_entries
		SET superseded_by = $1
		WHERE id = $2 AND org_id = $3
	`, newID, oldID, scope.OrgID)
	if err != nil {
		return fmt.Errorf("supersede %s -> %s: %w", oldID, newID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("entry %s not found in org %s", oldID, scope.OrgID)
	}
	return nil
}
