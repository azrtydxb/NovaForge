// Package knowledge owns the knowledge service's schema: persistent,
// per-repository project knowledge (decisions, patterns, incidents,
// corrections, and operational notes) that supersedes rather than
// overwrites, so a stale conclusion can never silently reappear in an
// agent's context.
package knowledge

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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
// match the caller's authz.Scope. Superseded entries cannot be overwritten.
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
		WHERE knowledge_entries.superseded_by IS NULL
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

// SearchSimilar is Search restricted to entries whose cosine similarity to
// query is at least minSimilarity. Search's top-k always returns the k
// least-distant entries, however far they are; handing those to an agent as
// "relevant knowledge" would present an unrelated decision as a related one.
func (s *Store) SearchSimilar(ctx context.Context, orgID, repoID uuid.UUID, query []float32, k int, minSimilarity float64) ([]Entry, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return nil, err
	}
	return s.queryEntries(ctx, `
		SELECT id, org_id, repo_id, key, kind, title, body, source_run_id, superseded_by, created_at
		FROM knowledge.knowledge_entries
		WHERE org_id = $2 AND repo_id = $3 AND superseded_by IS NULL AND embedding IS NOT NULL
		  AND 1 - (embedding <=> $1::vector) >= $5
		ORDER BY embedding <=> $1::vector
		LIMIT $4
	`, vectorLiteral(query), orgID, repoID, k, minSimilarity)
}

var textWordRe = regexp.MustCompile(`[A-Za-z0-9]{4,}`)

// SearchText returns up to k current entries sharing words with text, best
// match first, using PostgreSQL's English full-text search so "invoices"
// finds "invoice". It is the knowledge search that needs no embedding model:
// an entry recorded on a deployment without one — or while it was down — has
// no vector, and only this search can ever find it.
func (s *Store) SearchText(ctx context.Context, orgID, repoID uuid.UUID, text string, k int) ([]Entry, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return nil, err
	}
	// Words are reduced to [A-Za-z0-9] before they reach to_tsquery, so no
	// input can form query syntax; they are ORed, and ranking puts entries
	// sharing more of them first.
	seen := map[string]bool{}
	var terms []string
	for _, w := range textWordRe.FindAllString(text, -1) {
		w = strings.ToLower(w)
		if !seen[w] && len(terms) < 32 {
			seen[w] = true
			terms = append(terms, w)
		}
	}
	if len(terms) == 0 {
		return nil, nil
	}
	return s.queryEntries(ctx, `
		SELECT id, org_id, repo_id, key, kind, title, body, source_run_id, superseded_by, created_at
		FROM knowledge.knowledge_entries,
		     to_tsquery('english', $1) q
		WHERE org_id = $2 AND repo_id = $3 AND superseded_by IS NULL
		  AND to_tsvector('english', title || ' ' || body) @@ q
		ORDER BY ts_rank(to_tsvector('english', title || ' ' || body), q) DESC, created_at DESC
		LIMIT $4
	`, strings.Join(terms, " | "), orgID, repoID, k)
}

// List returns up to k current entries of repoID, newest first — what a
// person sees before searching for anything.
func (s *Store) List(ctx context.Context, orgID, repoID uuid.UUID, k int) ([]Entry, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return nil, err
	}
	return s.queryEntries(ctx, `
		SELECT id, org_id, repo_id, key, kind, title, body, source_run_id, superseded_by, created_at
		FROM knowledge.knowledge_entries
		WHERE org_id = $1 AND repo_id = $2 AND superseded_by IS NULL
		ORDER BY created_at DESC
		LIMIT $3
	`, orgID, repoID, k)
}

func (s *Store) queryEntries(ctx context.Context, sql string, args ...any) ([]Entry, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("knowledge query: %w", err)
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
	return out, rows.Err()
}

// ErrSupersessionConflict means the requested historical link is invalid.
var ErrSupersessionConflict = errors.New("invalid knowledge supersession")

// Supersede preserves immutable history within one repository. Repository-level
// serialization prevents concurrent opposite links from each observing no cycle.
func (s *Store) Supersede(ctx context.Context, oldID, newID uuid.UUID) error {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return err
	}
	if scope.OrgID == uuid.Nil || oldID == uuid.Nil || newID == uuid.Nil || oldID == newID {
		return ErrSupersessionConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var repo uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT repo_id FROM knowledge.knowledge_entries WHERE id=$1 AND org_id=$2`, oldID, scope.OrgID).Scan(&repo); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "knowledge-supersede:"+scope.OrgID.String()+":"+repo.String()); err != nil {
		return err
	}
	var existing *uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT superseded_by FROM knowledge.knowledge_entries WHERE id=$1 AND org_id=$2 AND repo_id=$3 FOR UPDATE`, oldID, scope.OrgID, repo).Scan(&existing); err != nil {
		return err
	}
	if existing != nil && *existing != newID {
		return ErrSupersessionConflict
	}
	// Follow only authorized rows, with a visited set so even corrupt legacy
	// cycles cannot hang the owner. A missing/foreign replacement is not trusted.
	seen := map[uuid.UUID]bool{oldID: true}
	next := &newID
	for next != nil {
		if seen[*next] || len(seen) > 10000 {
			return ErrSupersessionConflict
		}
		seen[*next] = true
		var successor *uuid.UUID
		if err = tx.QueryRow(ctx, `SELECT superseded_by FROM knowledge.knowledge_entries WHERE id=$1 AND org_id=$2 AND repo_id=$3`, *next, scope.OrgID, repo).Scan(&successor); err != nil {
			return err
		}
		next = successor
	}
	if existing != nil {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE knowledge.knowledge_entries SET superseded_by=$1 WHERE id=$2 AND org_id=$3 AND repo_id=$4`, newID, oldID, scope.OrgID, repo); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
