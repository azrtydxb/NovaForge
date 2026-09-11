// Package ctxasm assembles bounded, per-Work-Item context for an agent:
// candidates are gathered from six independent signals (lexical, symbol,
// dependency, semantic, history, and test), reranked against the Work
// Item's goal, and taken in score order until a token budget is spent. It
// is named ctxasm rather than "context" because the latter would shadow
// the standard library package of that name.
package ctxasm

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/knowledge"
	"github.com/novaforge/novaforge/internal/work"
)

// maxPerSignal bounds how many raw candidates any one of the six signals
// may contribute, so a broad match in one signal cannot crowd out the
// others or turn context assembly into a repository dump.
const maxPerSignal = 50

// Snippet is one candidate (or selected) piece of context: either a span of
// source text, or — for the history signal, which has no file span — a
// commit description. Signal names which of the six signals produced it.
type Snippet struct {
	Path      string
	StartLine int
	EndLine   int
	Text      string
	Signal    string
}

// Bundle is the bounded context assembled for a Work Item.
type Bundle struct {
	Files           []Snippet
	Knowledge       []knowledge.Entry
	Tests           []string
	TokensEstimated int
}

// Reranker ranks docs by relevance to query, returning one score per doc in
// the same order. It is backed by go-ai-sdk in production; tests use a
// deterministic in-process stub, a legitimate double for an external model.
type Reranker interface {
	Rank(ctx context.Context, query string, docs []string) ([]float32, error)
}

// Input is everything Assemble needs to build a Bundle for one Work Item.
type Input struct {
	OrgID       uuid.UUID
	RepoID      uuid.UUID
	WorkItem    work.Item
	TokenBudget int
	Git         gitv1.GitServiceClient
	Graph       *graph.Store
	Vectors     *graph.VectorStore
	Knowledge   *knowledge.Store
	Reranker    Reranker
}

// Assemble gathers candidates from six independent signals, reranks them
// against the Work Item's goal, and returns a Bundle whose token estimate
// (len(text)/4, running as candidates are taken in score order) never
// exceeds in.TokenBudget. Every signal tolerates its own failure — a stale
// or empty index degrades the bundle rather than failing Assemble, which
// only returns a non-nil error for something it cannot possibly recover
// from (an unusable Reranker together with no candidates at all does not
// even reach that point, since an empty candidate set short-circuits
// before Reranker is consulted).
func Assemble(ctx context.Context, in Input) (Bundle, error) {
	ctx = authz.WithScope(ctx, authz.Scope{OrgID: in.OrgID, ActorKind: "service"})

	keywords := extractKeywords(in.WorkItem.Goal)
	anchor := anchorEmbedding(ctx, in, keywords)

	symbolHits, symbolNodeIDs := symbolCandidates(ctx, in, keywords)

	var candidates []Snippet
	candidates = append(candidates, lexicalCandidates(ctx, in, keywords)...)
	candidates = append(candidates, symbolHits...)
	candidates = append(candidates, dependencyCandidates(ctx, in, symbolNodeIDs)...)
	candidates = append(candidates, semanticCandidates(ctx, in, anchor)...)
	candidates = append(candidates, historyCandidates(ctx, in)...)

	testHits, testPaths := testCandidates(ctx, in, symbolNodeIDs)
	candidates = append(candidates, testHits...)

	bundle := Bundle{
		Knowledge: knowledgeCandidates(ctx, in, anchor),
		Tests:     dedupeStrings(testPaths),
	}

	candidates = dedupeSnippets(candidates)
	if len(candidates) == 0 {
		return bundle, nil
	}

	docs := make([]string, len(candidates))
	for i, c := range candidates {
		docs[i] = c.Text
	}

	scores := rerank(ctx, in.Reranker, in.WorkItem.Goal, docs)

	order := make([]int, len(candidates))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })

	tokens := 0
	for _, idx := range order {
		c := candidates[idx]
		cost := len(c.Text) / 4
		if tokens+cost > in.TokenBudget {
			break
		}
		bundle.Files = append(bundle.Files, c)
		tokens += cost
	}
	bundle.TokensEstimated = tokens
	return bundle, nil
}

// rerank scores docs against query, falling back to a stable input-order
// ranking when the Reranker is unset or fails — a model outage degrades
// selection quality rather than failing context assembly outright.
func rerank(ctx context.Context, r Reranker, query string, docs []string) []float32 {
	fallback := func() []float32 {
		scores := make([]float32, len(docs))
		for i := range docs {
			scores[i] = float32(len(docs) - i)
		}
		return scores
	}
	if r == nil {
		return fallback()
	}
	scores, err := r.Rank(ctx, query, docs)
	if err != nil || len(scores) != len(docs) {
		if err != nil {
			log.Printf("ctxasm: rerank: %v", err)
		}
		return fallback()
	}
	return scores
}

var wordRe = regexp.MustCompile(`[A-Za-z0-9_]{4,}`)

// extractKeywords pulls distinct, lowercase words of at least 4 characters
// out of goal, capped at 12 so downstream queries stay bounded.
func extractKeywords(goal string) []string {
	words := wordRe.FindAllString(goal, -1)
	seen := make(map[string]bool, len(words))
	var out []string
	for _, w := range words {
		lw := strings.ToLower(w)
		if seen[lw] {
			continue
		}
		seen[lw] = true
		out = append(out, lw)
		if len(out) == 12 {
			break
		}
	}
	return out
}

// keywordPattern builds a POSIX case-insensitive alternation pattern
// (passed as a bound query parameter, never concatenated into SQL) matching
// any of keywords. Keywords are pre-filtered to [A-Za-z0-9_]+ by
// extractKeywords, so none of them can inject regex metacharacters.
func keywordPattern(keywords []string) string {
	return strings.Join(keywords, "|")
}

// lexicalCandidates finds code chunks whose text mentions one of keywords —
// a plain keyword/full-text signal, independent of the symbol graph or any
// embedding.
func lexicalCandidates(ctx context.Context, in Input, keywords []string) []Snippet {
	if len(keywords) == 0 || in.Graph == nil {
		return nil
	}
	rows, err := in.Graph.Pool().Query(ctx, `
		SELECT path, start_line, end_line, text
		FROM graph.code_chunks
		WHERE org_id = $1 AND repo_id = $2 AND text ~* $3
		LIMIT $4
	`, in.OrgID, in.RepoID, keywordPattern(keywords), maxPerSignal)
	if err != nil {
		log.Printf("ctxasm: lexical signal: %v", err)
		return nil
	}
	defer rows.Close()

	var out []Snippet
	for rows.Next() {
		var s Snippet
		if err := rows.Scan(&s.Path, &s.StartLine, &s.EndLine, &s.Text); err != nil {
			log.Printf("ctxasm: lexical signal scan: %v", err)
			return out
		}
		s.Signal = "lexical"
		out = append(out, s)
	}
	return out
}

// symbolCandidates finds graph symbol nodes whose name or key mentions one
// of keywords, returning both their Snippets (built from the symbol's
// signature) and their node ids, so the dependency and test signals can
// walk the graph from the same starting points.
func symbolCandidates(ctx context.Context, in Input, keywords []string) ([]Snippet, []uuid.UUID) {
	if len(keywords) == 0 || in.Graph == nil {
		return nil, nil
	}
	rows, err := in.Graph.Pool().Query(ctx, `
		SELECT id, attrs->>'path', attrs->>'start_line', attrs->>'end_line',
		       attrs->>'name', attrs->>'signature'
		FROM graph.graph_nodes
		WHERE org_id = $1 AND repo_id = $2 AND kind = 'symbol'
		  AND (key ~* $3 OR attrs->>'name' ~* $3)
		LIMIT $4
	`, in.OrgID, in.RepoID, keywordPattern(keywords), maxPerSignal)
	if err != nil {
		log.Printf("ctxasm: symbol signal: %v", err)
		return nil, nil
	}
	defer rows.Close()

	var snippets []Snippet
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		var path, startStr, endStr, name, sig string
		if err := rows.Scan(&id, &path, &startStr, &endStr, &name, &sig); err != nil {
			log.Printf("ctxasm: symbol signal scan: %v", err)
			return snippets, ids
		}
		start, _ := strconv.Atoi(startStr)
		end, _ := strconv.Atoi(endStr)
		text := sig
		if text == "" {
			text = name
		}
		snippets = append(snippets, Snippet{Path: path, StartLine: start, EndLine: end, Text: text, Signal: "symbol"})
		ids = append(ids, id)
	}
	return snippets, ids
}

// dependencyCandidates walks one hop of depends_on edges (in both
// directions) from each seed symbol node, surfacing the services, APIs, and
// other symbols that relate to the Work Item through the dependency graph
// rather than through text.
func dependencyCandidates(ctx context.Context, in Input, seeds []uuid.UUID) []Snippet {
	if len(seeds) == 0 || in.Graph == nil {
		return nil
	}
	var out []Snippet
	for _, id := range seeds {
		for _, dir := range []string{"in", "out"} {
			neighbours, err := in.Graph.Neighbours(ctx, id, "depends_on", dir)
			if err != nil {
				log.Printf("ctxasm: dependency signal: %v", err)
				continue
			}
			for _, n := range neighbours {
				text := n.Attrs["signature"]
				if text == "" {
					text = n.Attrs["name"]
				}
				if text == "" {
					text = n.Key
				}
				start, _ := strconv.Atoi(n.Attrs["start_line"])
				end, _ := strconv.Atoi(n.Attrs["end_line"])
				out = append(out, Snippet{Path: n.Attrs["path"], StartLine: start, EndLine: end, Text: text, Signal: "dependency"})
				if len(out) >= maxPerSignal {
					return out
				}
			}
		}
	}
	return out
}

// testCandidates walks tested_by edges from each seed symbol node,
// returning both Snippets for the candidate pool (tagged "test") and the
// plain list of test paths Bundle.Tests carries regardless of whether the
// reranker ultimately selects them — a Work Item's covering tests are
// worth surfacing even when the budget does not have room for their text.
func testCandidates(ctx context.Context, in Input, seeds []uuid.UUID) ([]Snippet, []string) {
	if len(seeds) == 0 || in.Graph == nil {
		return nil, nil
	}
	var snippets []Snippet
	var paths []string
	for _, id := range seeds {
		neighbours, err := in.Graph.Neighbours(ctx, id, "tested_by", "out")
		if err != nil {
			log.Printf("ctxasm: test signal: %v", err)
			continue
		}
		for _, n := range neighbours {
			path := n.Attrs["path"]
			if path == "" {
				path = n.Key
			}
			paths = append(paths, path)
			snippets = append(snippets, Snippet{Path: path, Text: "test: " + path, Signal: "test"})
			if len(snippets) >= maxPerSignal {
				return snippets, paths
			}
		}
	}
	return snippets, paths
}

// anchorEmbedding picks an anchor vector for the semantic and knowledge
// signals by reading the embedding already stored for a code chunk that
// matches keywords, rather than requiring ctxasm to embed the goal text
// itself — Assemble has no Embedder dependency, so semantic retrieval rides
// on whatever the indexer already embedded. Returns nil when nothing
// matches, which the semantic and knowledge signals treat as "skip me".
func anchorEmbedding(ctx context.Context, in Input, keywords []string) []float32 {
	if len(keywords) == 0 || in.Graph == nil {
		return nil
	}
	var embText string
	err := in.Graph.Pool().QueryRow(ctx, `
		SELECT embedding::text
		FROM graph.code_chunks
		WHERE org_id = $1 AND repo_id = $2 AND text ~* $3
		ORDER BY id
		LIMIT 1
	`, in.OrgID, in.RepoID, keywordPattern(keywords)).Scan(&embText)
	if err != nil {
		if !errorsIsNoRows(err) {
			log.Printf("ctxasm: anchor embedding: %v", err)
		}
		return nil
	}
	v, err := parseVectorLiteral(embText)
	if err != nil {
		log.Printf("ctxasm: parse anchor embedding: %v", err)
		return nil
	}
	return v
}

func errorsIsNoRows(err error) bool {
	return err == pgx.ErrNoRows
}

// parseVectorLiteral parses a pgvector text literal ("[0.1,0.2]") into
// []float32, mirroring graph's own unexported parser since that package's
// store.go and embed.go are off limits to change.
func parseVectorLiteral(s string) ([]float32, error) {
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

// semanticSimilarityFloor is the minimum cosine similarity a chunk must
// have to anchor before the semantic signal treats it as related. Without
// this, Search's top-k would still return the least-bad match even when
// nothing in the repository is actually close, letting a wholly unrelated
// file ride in on an empty index's worth of "nearest" results.
const semanticSimilarityFloor = 0.5

// semanticCandidates finds the code chunks nearest anchor by embedding
// distance — retrieval that has nothing to do with keyword overlap, so it
// surfaces conceptually related code lexical search would miss. A chunk
// below semanticSimilarityFloor is not "related", just the least-distant
// of whatever exists, so it is excluded rather than surfaced.
func semanticCandidates(ctx context.Context, in Input, anchor []float32) []Snippet {
	if len(anchor) == 0 || in.Vectors == nil {
		return nil
	}
	chunks, err := in.Vectors.Search(ctx, in.OrgID, in.RepoID, anchor, maxPerSignal)
	if err != nil {
		log.Printf("ctxasm: semantic signal: %v", err)
		return nil
	}
	out := make([]Snippet, 0, len(chunks))
	for _, c := range chunks {
		if c.Score < semanticSimilarityFloor {
			continue
		}
		out = append(out, Snippet{Path: c.Path, StartLine: c.StartLine, EndLine: c.EndLine, Text: c.Text, Signal: "semantic"})
	}
	return out
}

// historyCandidates surfaces recent commit messages as context: a Work
// Item's goal is often best explained by what previously changed nearby,
// which lexical and symbol search over the current tree cannot see.
func historyCandidates(ctx context.Context, in Input) []Snippet {
	if in.Git == nil {
		return nil
	}
	resp, err := in.Git.ListCommits(ctx, &gitv1.ListCommitsRequest{
		Repo:  in.RepoID.String(),
		Limit: maxPerSignal,
	})
	if err != nil {
		log.Printf("ctxasm: history signal: %v", err)
		return nil
	}
	out := make([]Snippet, 0, len(resp.GetCommits()))
	for _, c := range resp.GetCommits() {
		out = append(out, Snippet{
			Text:   fmt.Sprintf("%s: %s", c.GetSha(), c.GetMessage()),
			Signal: "history",
		})
	}
	return out
}

// knowledgeCandidates searches persistent project knowledge for entries
// near anchor, so a relevant prior decision or incident is included in the
// bundle rather than left for the agent to rediscover the hard way.
func knowledgeCandidates(ctx context.Context, in Input, anchor []float32) []knowledge.Entry {
	if len(anchor) == 0 || in.Knowledge == nil {
		return nil
	}
	entries, err := in.Knowledge.Search(ctx, in.OrgID, in.RepoID, anchor, 10)
	if err != nil {
		log.Printf("ctxasm: knowledge signal: %v", err)
		return nil
	}
	return entries
}

func dedupeSnippets(in []Snippet) []Snippet {
	seen := make(map[string]bool, len(in))
	out := make([]Snippet, 0, len(in))
	for _, s := range in {
		key := s.Signal + "|" + s.Path + "|" + strconv.Itoa(s.StartLine) + "|" + strconv.Itoa(s.EndLine) + "|" + s.Text
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// gatherCandidates re-runs the same six-signal collection Assemble does,
// without deduping or reranking, purely so tests can inspect which signals
// actually contributed before selection narrows the set down.
func gatherCandidates(ctx context.Context, in Input) []Snippet {
	ctx = authz.WithScope(ctx, authz.Scope{OrgID: in.OrgID, ActorKind: "service"})
	keywords := extractKeywords(in.WorkItem.Goal)
	anchor := anchorEmbedding(ctx, in, keywords)
	symbolHits, symbolNodeIDs := symbolCandidates(ctx, in, keywords)

	var all []Snippet
	all = append(all, lexicalCandidates(ctx, in, keywords)...)
	all = append(all, symbolHits...)
	all = append(all, dependencyCandidates(ctx, in, symbolNodeIDs)...)
	all = append(all, semanticCandidates(ctx, in, anchor)...)
	all = append(all, historyCandidates(ctx, in)...)
	testHits, _ := testCandidates(ctx, in, symbolNodeIDs)
	all = append(all, testHits...)
	return all
}
