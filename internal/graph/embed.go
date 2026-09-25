package graph

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	aisdk "github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/go-ai-sdk/providers/gateway"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/knowledge"
)

// EmbeddingDim is the fixed vector width code_chunks and knowledge_entries
// store, matching the vector(1024) columns of their migrations.
//
// It was 768, a width chosen with no model in mind. The embedding model this
// platform's gateway serves is bge-m3, which answers with 1024 dimensions, so
// Upsert refused every chunk the indexer produced and semantic search had
// nothing to search — one log line per file, indistinguishable from an idle
// index. A model of another width is a schema change; VerifyEmbedder reports
// a mismatch when the service starts.
//
// It is knowledge's constant, not a second copy: one embedder writes both
// tables, so two widths that could drift apart would be a way for one of them
// to go dark without the other.
const EmbeddingDim = knowledge.EmbeddingDim

const embeddingDim = EmbeddingDim

// Embedder turns source text into embedding vectors. NovaForge contains no
// provider-specific AI logic: every implementation goes through go-ai-sdk,
// so an air-gapped deployment can point it at a local model without any
// code path naming a hosted provider.
type Embedder interface {
	Embed(ctx context.Context, chunks []string) ([][]float32, error)
}

// EndpointEmbedder calls an embedding model through go-ai-sdk's gateway
// provider — the one provider every model call in NovaForge goes through —
// configured by EMBED_ENDPOINT (the gateway's base URL) and EMBED_MODEL (the
// model name it routes). An air-gapped deployment points EMBED_ENDPOINT at
// its own local gateway and nothing in NovaForge names a hosted provider.
type EndpointEmbedder struct {
	model provider.EmbeddingModel
}

// NewEndpointEmbedder builds an EndpointEmbedder from EMBED_ENDPOINT and
// EMBED_MODEL, returning an error if either is unset so a misconfigured
// deployment fails fast rather than silently calling a hosted default.
//
// The credential is EMBED_API_KEY, or AI_API_KEY when that is unset. The
// fallback is the fix for a real defect: embeddings are served by the same
// gateway as chat, behind the same credential, and the chart only ever sets
// AI_API_KEY — so the embedder called an authenticating gateway with no
// credential, every embed was refused, and nothing was ever indexed.
func NewEndpointEmbedder() (*EndpointEmbedder, error) {
	endpoint := os.Getenv("EMBED_ENDPOINT")
	model := os.Getenv("EMBED_MODEL")
	if endpoint == "" {
		return nil, fmt.Errorf("EMBED_ENDPOINT is not set")
	}
	if model == "" {
		return nil, fmt.Errorf("EMBED_MODEL is not set")
	}
	key := os.Getenv("EMBED_API_KEY")
	if key == "" {
		key = os.Getenv("AI_API_KEY")
	}
	p := gateway.New(gateway.WithBaseURL(endpoint), gateway.WithAPIKey(key))
	return &EndpointEmbedder{model: p.EmbeddingModel(model)}, nil
}

// VerifyEmbedder embeds one probe string and checks the model answers with
// EmbeddingDim dimensions. A model of the wrong width cannot store a single
// chunk, and finding that out per file, one log line per push, is how it went
// unnoticed; a service that checks at startup can say so once, loudly.
func VerifyEmbedder(ctx context.Context, e Embedder) error {
	vecs, err := e.Embed(ctx, []string{"dimension probe"})
	if err != nil {
		return fmt.Errorf("probe embedding model: %w", err)
	}
	if len(vecs) != 1 {
		return fmt.Errorf("probe embedding model: got %d vectors for one input", len(vecs))
	}
	if len(vecs[0]) != EmbeddingDim {
		return fmt.Errorf("%w: the model answers with %d dimensions but the schema stores %d; "+
			"point EMBED_MODEL at a %d-dimension model or migrate the vector columns",
			ErrEmbeddingWidth, len(vecs[0]), EmbeddingDim, EmbeddingDim)
	}
	return nil
}

// ErrEmbeddingWidth is VerifyEmbedder's error for a model whose vectors the
// schema cannot store. Unlike an unreachable gateway, it does not get better by
// waiting.
var ErrEmbeddingWidth = errors.New("embedding width mismatch")

// Embed satisfies Embedder by calling go-ai-sdk's EmbedMany against the
// configured OpenAI-compatible model, converting its []float64 embeddings
// to the []float32 this package stores.
func (e *EndpointEmbedder) Embed(ctx context.Context, chunks []string) ([][]float32, error) {
	if len(chunks) == 0 {
		return nil, nil
	}
	result, err := aisdk.EmbedMany(ctx, aisdk.EmbedManyOpts{
		Model:  e.model,
		Values: chunks,
	})
	if err != nil {
		return nil, fmt.Errorf("embed %d chunks: %w", len(chunks), err)
	}
	out := make([][]float32, len(result.Embeddings))
	for i, v := range result.Embeddings {
		out[i] = toFloat32(v)
	}
	return out, nil
}

func toFloat32(v []float64) []float32 {
	out := make([]float32, len(v))
	for i, f := range v {
		out[i] = float32(f)
	}
	return out
}

// Chunk is a stored code chunk with its embedding, or (from Search) a
// result carrying its similarity Score.
type Chunk struct {
	ID        uuid.UUID
	Path      string
	StartLine int
	EndLine   int
	Text      string
	Embedding []float32
	Score     float32
}

// VectorStore provides access to the code_chunks table. Every method
// derives its org_id predicate from the caller's authz.Scope, checked
// against the explicit orgID argument — no query is satisfiable without it.
type VectorStore struct {
	pool *pgxpool.Pool
}

// NewVectorStore wraps pool as a graph.VectorStore.
func NewVectorStore(pool *pgxpool.Pool) *VectorStore {
	return &VectorStore{pool: pool}
}

// vectorLiteral formats v as a pgvector text literal, e.g. "[0.1,0.2]".
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

// parseVector parses a pgvector text literal back into []float32.
func parseVector(s string) ([]float32, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]float32, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, fmt.Errorf("parse vector component %q: %w", p, err)
		}
		out[i] = float32(f)
	}
	return out, nil
}

// Upsert atomically replaces every chunk stored for path with chunks: it
// deletes the path's existing rows and inserts the new set in one
// transaction, so re-embedding a file never leaves stale chunks behind. An
// embedder failure upstream of Upsert (callers compute chunks' Embedding
// before calling it) never reaches here, so the relational index for path
// stays exactly as it was before the failed attempt.
func (vs *VectorStore) Upsert(ctx context.Context, orgID, repoID uuid.UUID, path string, chunks []Chunk) error {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return err
	}
	for _, c := range chunks {
		if len(c.Embedding) != embeddingDim {
			return fmt.Errorf("chunk %s for path %s: embedding has %d dimensions, want %d", c.ID, path, len(c.Embedding), embeddingDim)
		}
	}

	tx, err := beginIndexWrite(ctx, vs.pool, orgID, repoID)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		DELETE FROM graph.code_chunks WHERE org_id = $1 AND repo_id = $2 AND path = $3
	`, orgID, repoID, path); err != nil {
		return fmt.Errorf("delete stale chunks for %s: %w", path, err)
	}

	for _, c := range chunks {
		id := c.ID
		if id == uuid.Nil {
			id = uuid.New()
		}
		// c.Path defaults to the file this batch belongs to, but is stored
		// verbatim so a caller may distinguish chunks within the same
		// Upsert call (Search results always span potentially many files).
		chunkPath := c.Path
		if chunkPath == "" {
			chunkPath = path
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO graph.code_chunks (id, org_id, repo_id, path, start_line, end_line, text, embedding)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::vector)
		`, id, orgID, repoID, chunkPath, c.StartLine, c.EndLine, c.Text, vectorLiteral(c.Embedding)); err != nil {
			return fmt.Errorf("insert chunk for %s: %w", chunkPath, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Search returns the k chunks nearest query by cosine distance, nearest
// first, restricted to orgID and repoID.
func (vs *VectorStore) Search(ctx context.Context, orgID, repoID uuid.UUID, query []float32, k int) ([]Chunk, error) {
	if err := authz.RequireOrg(ctx, orgID); err != nil {
		return nil, err
	}

	rows, err := vs.pool.Query(ctx, `
		SELECT id, path, start_line, end_line, text, embedding::text,
		       1 - (embedding <=> $1::vector) AS score
		FROM graph.code_chunks
		WHERE org_id = $2 AND repo_id = $3
		ORDER BY embedding <=> $1::vector
		LIMIT $4
	`, vectorLiteral(query), orgID, repoID, k)
	if err != nil {
		return nil, fmt.Errorf("search query: %w", err)
	}
	defer rows.Close()

	var out []Chunk
	for rows.Next() {
		var c Chunk
		var embText string
		if err := rows.Scan(&c.ID, &c.Path, &c.StartLine, &c.EndLine, &c.Text, &embText, &c.Score); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		c.Embedding, err = parseVector(embText)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
