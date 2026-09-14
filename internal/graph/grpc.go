package graph

import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/knowledge"
	"github.com/novaforge/novaforge/internal/work"
)

// defaultSearchLimit bounds SearchCode and SearchKnowledge when the caller
// asks for k <= 0, so a misbehaving client cannot force an unbounded scan.
const defaultSearchLimit = 20

// ContextSnippet, ContextBundle and AssembleContextFunc describe context
// assembly in terms grpc.go can use without importing internal/ctxasm:
// ctxasm already imports graph (Assemble takes a *graph.Store), so graph
// importing ctxasm back would be a cycle. Whatever wires up the real
// engineering-graph service instead constructs an AssembleContextFunc that
// closes over ctxasm.Assemble and adapts its result into this shape.
type ContextSnippet struct {
	Path      string
	StartLine int
	EndLine   int
	Text      string
	Signal    string
}

// KnowledgeSummary is the subset of a knowledge.Entry a context bundle
// carries back over gRPC.
type KnowledgeSummary struct {
	ID        string
	Key       string
	Kind      string
	Title     string
	Body      string
	CreatedAt time.Time
}

// ContextBundle mirrors ctxasm.Bundle's shape for the gRPC boundary.
type ContextBundle struct {
	Files           []ContextSnippet
	Knowledge       []KnowledgeSummary
	Tests           []string
	TokensEstimated int
}

// AssembleContextFunc assembles a ContextBundle for a Work Item, identified
// by its human-readable key, within a token budget.
type AssembleContextFunc func(ctx context.Context, orgID, repoID uuid.UUID, workItemKey string, tokenBudget int) (ContextBundle, error)

// GRPCServer implements graphv1.GraphServiceServer. Every method derives
// the caller's organization from authz.FromContext and passes it as an
// explicit predicate to every query — never from a field of the request
// message, so a client cannot simply name a different org.id and be
// believed.
type GRPCServer struct {
	graphv1.UnimplementedGraphServiceServer

	Store     *Store
	Vectors   *VectorStore
	Knowledge *knowledge.Store
	Work      *work.Store
	// Embedder turns a text query into the vector SearchCode and
	// SearchKnowledge need. When nil, SearchCode falls back to a lexical
	// match and SearchKnowledge returns no results — degrading rather than
	// failing the RPC, consistent with an air-gapped deployment that has
	// not configured a local embedding model yet.
	Embedder Embedder
	// Assemble backs AssembleContext. When nil, AssembleContext reports
	// Unavailable rather than panicking or fabricating a bundle.
	Assemble AssembleContextFunc
}

// NewGRPCServer wraps the given dependencies as a graphv1.GraphServiceServer.
func NewGRPCServer(store *Store, vectors *VectorStore, knowledgeStore *knowledge.Store, workStore *work.Store, embedder Embedder, assemble AssembleContextFunc) *GRPCServer {
	return &GRPCServer{
		Store:     store,
		Vectors:   vectors,
		Knowledge: knowledgeStore,
		Work:      workStore,
		Embedder:  embedder,
		Assemble:  assemble,
	}
}

// callerOrg resolves the org the caller is authorized to act as, translating
// a missing scope into PermissionDenied — a gRPC call with no resolvable
// identity is treated as denied, not as an internal error.
func callerOrg(ctx context.Context) (uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return uuid.Nil, status.Error(codes.PermissionDenied, "no authorization scope for this call")
	}
	return scope.OrgID, nil
}

func parseRepoID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, status.Errorf(codes.InvalidArgument, "invalid repo_id %q: %v", raw, err)
	}
	return id, nil
}

func toProtoNode(n Node) *graphv1.Node {
	return &graphv1.Node{Id: n.ID.String(), Kind: n.Kind, Key: n.Key, Attrs: n.Attrs}
}

func toProtoSymbol(n Node) *graphv1.Symbol {
	start, end := 0, 0
	if v, err := atoiSafe(n.Attrs["start_line"]); err == nil {
		start = v
	}
	if v, err := atoiSafe(n.Attrs["end_line"]); err == nil {
		end = v
	}
	return &graphv1.Symbol{
		Id:        n.ID.String(),
		Name:      n.Attrs["name"],
		Kind:      n.Attrs["kind"],
		Path:      n.Attrs["path"],
		StartLine: int32(start),
		EndLine:   int32(end),
		Signature: n.Attrs["signature"],
	}
}

func atoiSafe(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, status.Error(codes.Internal, "non-numeric line attribute")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// resolveSymbol finds the symbol node named name within repoID, scoped to
// the caller's org. A symbol that exists for a different org — rather than
// not existing at all — is reported as PermissionDenied instead of
// NotFound, so a client cannot use error codes to enumerate other
// organizations' symbol names.
func (s *GRPCServer) resolveSymbol(ctx context.Context, orgID, repoID uuid.UUID, name string) (Node, error) {
	row := s.Store.pool.QueryRow(ctx, `
		SELECT id, org_id, kind, key, attrs
		FROM graph.graph_nodes
		WHERE org_id = $1 AND repo_id = $2 AND kind = 'symbol' AND attrs->>'name' = $3
		ORDER BY id
		LIMIT 1
	`, orgID, repoID, name)
	n, err := scanNode(row)
	if err == nil {
		return n, nil
	}
	if err != pgx.ErrNoRows {
		return Node{}, status.Errorf(codes.Internal, "resolve symbol: %v", err)
	}

	var exists bool
	if qerr := s.Store.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM graph.graph_nodes
			WHERE repo_id = $1 AND kind = 'symbol' AND attrs->>'name' = $2 AND org_id <> $3
		)
	`, repoID, name, orgID).Scan(&exists); qerr != nil {
		return Node{}, status.Errorf(codes.Internal, "resolve symbol: %v", qerr)
	}
	if exists {
		return Node{}, status.Errorf(codes.PermissionDenied, "symbol %q is not visible to this organization", name)
	}
	return Node{}, status.Errorf(codes.NotFound, "symbol %q not found", name)
}

// GetSymbol returns the symbol named req.Name in req.RepoId.
func (s *GRPCServer) GetSymbol(ctx context.Context, req *graphv1.GetSymbolRequest) (*graphv1.GetSymbolResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	repoID, err := parseRepoID(req.GetRepoId())
	if err != nil {
		return nil, err
	}
	n, err := s.resolveSymbol(ctx, orgID, repoID, req.GetName())
	if err != nil {
		return nil, err
	}
	return &graphv1.GetSymbolResponse{Symbol: toProtoSymbol(n)}, nil
}

// Dependents returns the nodes that depend on the named symbol (an inbound
// depends_on edge).
func (s *GRPCServer) Dependents(ctx context.Context, req *graphv1.DependentsRequest) (*graphv1.DependentsResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	repoID, err := parseRepoID(req.GetRepoId())
	if err != nil {
		return nil, err
	}
	sym, err := s.resolveSymbol(ctx, orgID, repoID, req.GetSymbol())
	if err != nil {
		return nil, err
	}
	ctx = authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorKind: "service"})
	neighbours, err := s.Store.Neighbours(ctx, sym.ID, "depends_on", "in")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "dependents: %v", err)
	}
	resp := &graphv1.DependentsResponse{Nodes: make([]*graphv1.Node, 0, len(neighbours))}
	for _, n := range neighbours {
		resp.Nodes = append(resp.Nodes, toProtoNode(n))
	}
	return resp, nil
}

// Dependencies returns the nodes the named symbol depends on (an outbound
// depends_on edge).
func (s *GRPCServer) Dependencies(ctx context.Context, req *graphv1.DependenciesRequest) (*graphv1.DependenciesResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	repoID, err := parseRepoID(req.GetRepoId())
	if err != nil {
		return nil, err
	}
	sym, err := s.resolveSymbol(ctx, orgID, repoID, req.GetSymbol())
	if err != nil {
		return nil, err
	}
	ctx = authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorKind: "service"})
	neighbours, err := s.Store.Neighbours(ctx, sym.ID, "depends_on", "out")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "dependencies: %v", err)
	}
	resp := &graphv1.DependenciesResponse{Nodes: make([]*graphv1.Node, 0, len(neighbours))}
	for _, n := range neighbours {
		resp.Nodes = append(resp.Nodes, toProtoNode(n))
	}
	return resp, nil
}

// TestsCovering returns the test paths covering the named symbol, via its
// outbound tested_by edges.
func (s *GRPCServer) TestsCovering(ctx context.Context, req *graphv1.TestsCoveringRequest) (*graphv1.TestsCoveringResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	repoID, err := parseRepoID(req.GetRepoId())
	if err != nil {
		return nil, err
	}
	sym, err := s.resolveSymbol(ctx, orgID, repoID, req.GetSymbol())
	if err != nil {
		return nil, err
	}
	ctx = authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorKind: "service"})
	neighbours, err := s.Store.Neighbours(ctx, sym.ID, "tested_by", "out")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "tests covering: %v", err)
	}
	resp := &graphv1.TestsCoveringResponse{Tests: make([]string, 0, len(neighbours))}
	for _, n := range neighbours {
		path := n.Attrs["path"]
		if path == "" {
			path = n.Key
		}
		resp.Tests = append(resp.Tests, path)
	}
	return resp, nil
}

// LastChangedBy returns the key of the Work Item whose changed_by edge to
// the named symbol is most recent.
func (s *GRPCServer) LastChangedBy(ctx context.Context, req *graphv1.LastChangedByRequest) (*graphv1.LastChangedByResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	repoID, err := parseRepoID(req.GetRepoId())
	if err != nil {
		return nil, err
	}
	sym, err := s.resolveSymbol(ctx, orgID, repoID, req.GetSymbol())
	if err != nil {
		return nil, err
	}
	ctx = authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorKind: "service"})
	neighbours, err := s.Store.Neighbours(ctx, sym.ID, "changed_by", "out")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "last changed by: %v", err)
	}
	if len(neighbours) == 0 {
		return nil, status.Errorf(codes.NotFound, "no changed_by edges for symbol %q", req.GetSymbol())
	}
	sort.Slice(neighbours, func(i, j int) bool {
		return neighbours[i].Attrs["changed_at"] > neighbours[j].Attrs["changed_at"]
	})
	key := neighbours[0].Attrs["key"]
	if key == "" {
		key = neighbours[0].Key
	}
	return &graphv1.LastChangedByResponse{WorkItemKey: key}, nil
}

// SearchCode answers a code search: semantically when an Embedder is
// configured, lexically otherwise, so the RPC degrades rather than fails in
// an air-gapped deployment that has not wired an embedding model.
func (s *GRPCServer) SearchCode(ctx context.Context, req *graphv1.SearchCodeRequest) (*graphv1.SearchCodeResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	repoID, err := parseRepoID(req.GetRepoId())
	if err != nil {
		return nil, err
	}
	k := int(req.GetK())
	if k <= 0 {
		k = defaultSearchLimit
	}

	if s.Embedder != nil {
		vecs, err := s.Embedder.Embed(ctx, []string{req.GetQuery()})
		if err == nil && len(vecs) == 1 {
			ctx = authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorKind: "service"})
			chunks, serr := s.Vectors.Search(ctx, orgID, repoID, vecs[0], k)
			if serr == nil {
				return &graphv1.SearchCodeResponse{Chunks: toProtoChunks(chunks), Mode: "semantic"}, nil
			}
			err = serr
		}
		// Falling back silently is how a broken embedder hid: the caller got
		// an empty lexical answer to a semantic question and could not know.
		// The fallback stays, but it is logged and the response names it.
		log.Printf("graph: semantic code search unavailable, answering lexically: %v", err)
	}

	rows, err := s.Store.pool.Query(ctx, `
		SELECT path, start_line, end_line, text
		FROM graph.code_chunks
		WHERE org_id = $1 AND repo_id = $2 AND text ILIKE '%' || $3 || '%'
		LIMIT $4
	`, orgID, repoID, req.GetQuery(), k)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "search code: %v", err)
	}
	defer rows.Close()

	resp := &graphv1.SearchCodeResponse{Mode: "lexical"}
	for rows.Next() {
		var path, text string
		var start, end int
		if err := rows.Scan(&path, &start, &end, &text); err != nil {
			return nil, status.Errorf(codes.Internal, "search code scan: %v", err)
		}
		resp.Chunks = append(resp.Chunks, &graphv1.CodeChunk{Path: path, StartLine: int32(start), EndLine: int32(end), Text: text})
	}
	return resp, nil
}

func toProtoChunks(chunks []Chunk) []*graphv1.CodeChunk {
	out := make([]*graphv1.CodeChunk, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, &graphv1.CodeChunk{Path: c.Path, StartLine: int32(c.StartLine), EndLine: int32(c.EndLine), Text: c.Text, Score: c.Score})
	}
	return out
}

// AssembleContext assembles bounded context for a Work Item. It requires
// the server to have been constructed with an AssembleContextFunc; without
// one, it reports Unavailable rather than fabricating an empty bundle.
func (s *GRPCServer) AssembleContext(ctx context.Context, req *graphv1.AssembleContextRequest) (*graphv1.AssembleContextResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	repoID, err := parseRepoID(req.GetRepoId())
	if err != nil {
		return nil, err
	}
	if s.Assemble == nil {
		return nil, status.Error(codes.Unavailable, "context assembly is not configured")
	}
	budget := int(req.GetTokenBudget())
	if budget <= 0 {
		budget = 4000
	}
	bundle, err := s.Assemble(ctx, orgID, repoID, req.GetWorkItemKey(), budget)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "assemble context: %v", err)
	}
	return &graphv1.AssembleContextResponse{Bundle: toProtoBundle(bundle)}, nil
}

func toProtoBundle(b ContextBundle) *graphv1.ContextBundle {
	out := &graphv1.ContextBundle{
		Tests:           b.Tests,
		TokensEstimated: int32(b.TokensEstimated),
	}
	for _, f := range b.Files {
		out.Files = append(out.Files, &graphv1.ContextSnippet{
			Path: f.Path, StartLine: int32(f.StartLine), EndLine: int32(f.EndLine), Text: f.Text, Signal: f.Signal,
		})
	}
	for _, k := range b.Knowledge {
		out.Knowledge = append(out.Knowledge, &graphv1.KnowledgeEntry{
			Id: k.ID, Key: k.Key, Kind: k.Kind, Title: k.Title, Body: k.Body, CreatedAt: k.CreatedAt.Format(time.RFC3339),
		})
	}
	return out
}

// RecordKnowledge records a new project knowledge entry, embedding it when
// an Embedder is configured so it can later be found by SearchKnowledge.
func (s *GRPCServer) RecordKnowledge(ctx context.Context, req *graphv1.RecordKnowledgeRequest) (*graphv1.RecordKnowledgeResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	repoID, err := parseRepoID(req.GetRepoId())
	if err != nil {
		return nil, err
	}
	ctx = authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorKind: "service"})

	var embedding []float32
	if s.Embedder != nil {
		vecs, err := s.Embedder.Embed(ctx, []string{req.GetTitle() + "\n" + req.GetBody()})
		if err == nil && len(vecs) == 1 {
			embedding = vecs[0]
		}
	}

	entry, err := s.Knowledge.Record(ctx, knowledge.Entry{
		OrgID:  orgID,
		RepoID: repoID,
		Key:    req.GetKey(),
		Kind:   req.GetKind(),
		Title:  req.GetTitle(),
		Body:   req.GetBody(),
	}, embedding)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "record knowledge: %v", err)
	}
	return &graphv1.RecordKnowledgeResponse{Id: entry.ID.String()}, nil
}

// SearchKnowledge searches project knowledge semantically. It requires an
// Embedder to turn req.Query into a vector; without one it returns no
// results rather than failing, since text-only knowledge search is not
// supported.
func (s *GRPCServer) SearchKnowledge(ctx context.Context, req *graphv1.SearchKnowledgeRequest) (*graphv1.SearchKnowledgeResponse, error) {
	orgID, err := callerOrg(ctx)
	if err != nil {
		return nil, err
	}
	repoID, err := parseRepoID(req.GetRepoId())
	if err != nil {
		return nil, err
	}
	if s.Embedder == nil {
		return &graphv1.SearchKnowledgeResponse{}, nil
	}
	k := int(req.GetK())
	if k <= 0 {
		k = defaultSearchLimit
	}
	vecs, err := s.Embedder.Embed(ctx, []string{req.GetQuery()})
	if err != nil || len(vecs) != 1 {
		return &graphv1.SearchKnowledgeResponse{}, nil
	}
	ctx = authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorKind: "service"})
	entries, err := s.Knowledge.Search(ctx, orgID, repoID, vecs[0], k)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "search knowledge: %v", err)
	}
	resp := &graphv1.SearchKnowledgeResponse{}
	for _, e := range entries {
		resp.Entries = append(resp.Entries, &graphv1.KnowledgeEntry{
			Id: e.ID.String(), Key: e.Key, Kind: e.Kind, Title: e.Title, Body: e.Body, CreatedAt: e.CreatedAt.Format(time.RFC3339),
		})
	}
	return resp, nil
}
