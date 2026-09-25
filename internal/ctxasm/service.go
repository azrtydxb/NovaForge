package ctxasm

import (
	"context"
	"fmt"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"google.golang.org/grpc"
	"time"

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

// NewRPCAssembleFunc resolves intent through its owner. The graph connection has
// no authority to query work tables, even when both schemas share a database.
func NewRPCAssembleFunc(gitClient gitv1.GitServiceClient, graphStore *graph.Store, vectors *graph.VectorStore, knowledgeStore *knowledge.Store, client workv1.WorkServiceClient, embedder graph.Embedder, hmacSecret string, ranker Reranker) graph.AssembleContextFunc {
	return func(ctx context.Context, orgID, repoID uuid.UUID, key string, budget int) (graph.ContextBundle, error) {
		if err := authz.RequireOrg(ctx, orgID); err != nil {
			return graph.ContextBundle{}, err
		}
		tok, err := svcauth.Mint(hmacSecret, "engineering-graph", orgID, svcauth.DefaultTTL)
		if err != nil {
			return graph.ContextBundle{}, err
		}
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+tok, "x-novaforge-org", orgID.String()))
		item, err := resolveWorkItem(ctx, client, orgID, repoID, key)
		if err != nil {
			return graph.ContextBundle{}, err
		}
		b, err := Assemble(ctx, Input{OrgID: orgID, RepoID: repoID, WorkItem: item, TokenBudget: budget, Git: gitClient, Graph: graphStore, Vectors: vectors, Knowledge: knowledgeStore, Embedder: embedder, Reranker: ranker})
		return ToGraphBundle(b), err
	}
}

type workReader interface {
	GetItem(context.Context, *workv1.GetItemRequest, ...grpc.CallOption) (*workv1.GetItemResponse, error)
}

func resolveWorkItem(ctx context.Context, client workReader, org, repo uuid.UUID, key string) (work.Item, error) {
	if client == nil {
		return work.Item{}, fmt.Errorf("work service unavailable")
	}
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := client.GetItem(call, &workv1.GetItemRequest{Key: key})
	if err != nil {
		return work.Item{}, fmt.Errorf("resolve work item: %w", err)
	}
	item := response.GetItem()
	if item.GetOrgId() != org.String() || item.GetRepoId() != repo.String() {
		return work.Item{}, fmt.Errorf("work item repository scope mismatch")
	}
	return work.Item{Goal: item.GetGoal(), Acceptance: item.GetAcceptance()}, nil
}
