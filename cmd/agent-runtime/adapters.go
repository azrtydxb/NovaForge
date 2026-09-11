package main

import (
	"context"
	"fmt"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/tools"
)

// gitAdapter satisfies tools.GitClient over the git-platform gRPC client.
// git-platform's current RPC surface (proto/novaforge/git/v1/git.proto,
// which this service does not own and this task does not touch) has no
// symbol, dependency-graph, full-text search, or commit-creation RPCs yet
// — tools.GitClient's own doc comment calls this out as future work. Search,
// GetSymbol, and GetDependencies are therefore answered honestly as
// unavailable rather than faked; ReadFile and Diff are fully real, backed
// by GetBlob and GetDiff.
type gitAdapter struct {
	git gitv1.GitServiceClient
}

func newGitAdapter(git gitv1.GitServiceClient) tools.GitClient {
	return &gitAdapter{git: git}
}

func (a *gitAdapter) Search(ctx context.Context, repo, query string) ([]tools.SearchHit, error) {
	return nil, fmt.Errorf("git-platform has no full-text search RPC yet")
}

func (a *gitAdapter) ReadFile(ctx context.Context, repo, ref, path string) ([]byte, error) {
	resp, err := a.git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repo, Ref: ref, Path: path})
	if err != nil {
		return nil, fmt.Errorf("read %s@%s:%s: %w", repo, ref, path, err)
	}
	return resp.GetContent(), nil
}

func (a *gitAdapter) GetSymbol(ctx context.Context, repo, ref, symbol string) (tools.SymbolInfo, error) {
	return tools.SymbolInfo{}, fmt.Errorf("symbol lookup requires the engineering-graph service, not wired into this tool adapter")
}

func (a *gitAdapter) GetDependencies(ctx context.Context, repo, ref, path string) ([]string, error) {
	return nil, fmt.Errorf("dependency lookup requires the engineering-graph service, not wired into this tool adapter")
}

func (a *gitAdapter) Diff(ctx context.Context, repo, from, to string) (string, error) {
	resp, err := a.git.GetDiff(ctx, &gitv1.GetDiffRequest{Repo: repo, From: from, To: to})
	if err != nil {
		return "", fmt.Errorf("diff %s %s..%s: %w", repo, from, to, err)
	}
	return resp.GetUnified(), nil
}

func (a *gitAdapter) Commit(ctx context.Context, repo, branch, message string, files map[string]string) (string, error) {
	return "", fmt.Errorf("git-platform has no commit-creation RPC yet; this run's changes must be committed through a workspace-local git client instead")
}

// workAdapter satisfies tools.WorkClient over the work-reviews gRPC client.
// GetItem answers work.get for real; work.comment has no backing RPC on
// WorkService (comments exist only on reviews.Run, addressed via
// AddComment on a different service) and is answered honestly as
// unavailable.
type workAdapter struct {
	work workv1.WorkServiceClient
}

func newWorkAdapter(work workv1.WorkServiceClient) tools.WorkClient {
	return &workAdapter{work: work}
}

func (a *workAdapter) Get(ctx context.Context, workItemID string) (tools.WorkItemSummary, error) {
	resp, err := a.work.GetItem(ctx, &workv1.GetItemRequest{Id: workItemID})
	if err != nil {
		return tools.WorkItemSummary{}, fmt.Errorf("get work item %s: %w", workItemID, err)
	}
	item := resp.GetItem()
	return tools.WorkItemSummary{
		ID:    item.GetId(),
		Key:   item.GetKey(),
		Goal:  item.GetGoal(),
		State: item.GetState(),
	}, nil
}

func (a *workAdapter) Comment(ctx context.Context, workItemID, body string) error {
	return fmt.Errorf("work service has no comment RPC on Work Items yet; comments are only addressable on reviews.Run")
}
