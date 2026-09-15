package ctxasm

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/knowledge"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/novaforge/novaforge/internal/work"
)

// NewAssembleFunc adapts Assemble into the graph service's AssembleContext
// RPC: it resolves the named Work Item, runs the six-signal assembly, and
// converts the result into graph's gRPC-boundary shapes.
//
// The history signal reads commits through the git service, which refuses an
// anonymous caller. engineering-graph dialled git-platform with no credential
// at all, so on the cluster that signal was refused on every call and
// silently contributed nothing; it now presents a service token for exactly
// the organization whose context it is assembling, as the indexer does.
func NewAssembleFunc(gitClient gitv1.GitServiceClient, graphStore *graph.Store, vectors *graph.VectorStore, knowledgeStore *knowledge.Store, workStore *work.Store, embedder graph.Embedder, hmacSecret string) graph.AssembleContextFunc {
	return func(ctx context.Context, orgID, repoID uuid.UUID, workItemKey string, tokenBudget int) (graph.ContextBundle, error) {
		item, err := workStore.GetByKey(ctx, orgID, workItemKey)
		if err != nil {
			return graph.ContextBundle{}, fmt.Errorf("resolve work item %q: %w", workItemKey, err)
		}
		tok, err := svcauth.Mint(hmacSecret, "engineering-graph", orgID, svcauth.DefaultTTL)
		if err != nil {
			return graph.ContextBundle{}, fmt.Errorf("mint service token: %w", err)
		}
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(
			"authorization", "Bearer "+tok,
			"x-novaforge-org", orgID.String(),
		))
		bundle, err := Assemble(ctx, Input{
			OrgID:       orgID,
			RepoID:      repoID,
			WorkItem:    item,
			TokenBudget: tokenBudget,
			Git:         gitClient,
			Graph:       graphStore,
			Vectors:     vectors,
			Knowledge:   knowledgeStore,
			Embedder:    embedder,
		})
		if err != nil {
			return graph.ContextBundle{}, err
		}
		return ToGraphBundle(bundle), nil
	}
}

// ToGraphBundle converts a Bundle into the graph service's wire shape.
func ToGraphBundle(b Bundle) graph.ContextBundle {
	files := make([]graph.ContextSnippet, len(b.Files))
	for i, f := range b.Files {
		files[i] = graph.ContextSnippet{Path: f.Path, StartLine: f.StartLine, EndLine: f.EndLine, Text: f.Text, Signal: f.Signal}
	}
	entries := make([]graph.KnowledgeSummary, len(b.Knowledge))
	for i, e := range b.Knowledge {
		entries[i] = graph.KnowledgeSummary{ID: e.ID.String(), Key: e.Key, Kind: e.Kind, Title: e.Title, Body: e.Body, CreatedAt: e.CreatedAt}
		if e.SourceRunID != nil {
			entries[i].SourceRunID = e.SourceRunID.String()
		}
	}
	return graph.ContextBundle{Files: files, Knowledge: entries, Tests: b.Tests, TokensEstimated: b.TokensEstimated}
}
