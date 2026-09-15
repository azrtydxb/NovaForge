package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/tools"
)

// gitAdapter satisfies tools.GitClient over two services, because the
// repo.* tools span two of them: content comes from git-platform (GetBlob,
// GetDiff) and code intelligence from engineering-graph (SearchCode,
// GetSymbol, Dependencies), which is where the parsed symbol and dependency
// graph actually lives. graph may be nil when the deployment has no graph
// service configured, in which case the three tools it backs report
// themselves unavailable rather than returning an empty result that would
// read as "nothing matched".
type gitAdapter struct {
	git   gitv1.GitServiceClient
	graph graphv1.GraphServiceClient
}

func newGitAdapter(git gitv1.GitServiceClient, graph graphv1.GraphServiceClient) tools.GitClient {
	return &gitAdapter{git: git, graph: graph}
}

// searchHitLimit bounds how many chunks repo.search asks the graph for. An
// agent reads results into a bounded context window, so an unbounded result
// set would be spent on the tail nobody reads.
const searchHitLimit = 20

func (a *gitAdapter) Search(ctx context.Context, repo, query string) ([]tools.SearchHit, error) {
	if a.graph == nil {
		return nil, fmt.Errorf("repo.search needs the engineering-graph service, which this deployment has not configured")
	}
	resp, err := a.graph.SearchCode(ctx, &graphv1.SearchCodeRequest{RepoId: repo, Query: query, K: searchHitLimit})
	if err != nil {
		return nil, fmt.Errorf("search %s for %q: %w", repo, query, err)
	}
	hits := make([]tools.SearchHit, 0, len(resp.GetChunks()))
	for _, c := range resp.GetChunks() {
		hits = append(hits, tools.SearchHit{
			Path: c.GetPath(),
			Line: int(c.GetStartLine()),
			Text: c.GetText(),
		})
	}
	return hits, nil
}

func (a *gitAdapter) ReadFile(ctx context.Context, repo, ref, path string) ([]byte, error) {
	resp, err := a.git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: repo, Ref: ref, Path: path})
	if err != nil {
		return nil, fmt.Errorf("read %s@%s:%s: %w", repo, ref, path, err)
	}
	return resp.GetContent(), nil
}

// GetSymbol answers from the graph's indexed symbol table. ref is not a
// parameter of the graph's GetSymbol: the graph indexes a repository's
// default branch, so a symbol lookup pinned to an arbitrary ref would be
// answered from the indexed ref anyway — saying so is better than silently
// ignoring the argument.
func (a *gitAdapter) GetSymbol(ctx context.Context, repo, ref, symbol string) (tools.SymbolInfo, error) {
	if a.graph == nil {
		return tools.SymbolInfo{}, fmt.Errorf("repo.get_symbol needs the engineering-graph service, which this deployment has not configured")
	}
	resp, err := a.graph.GetSymbol(ctx, &graphv1.GetSymbolRequest{RepoId: repo, Name: symbol})
	if err != nil {
		return tools.SymbolInfo{}, fmt.Errorf("get symbol %q in %s: %w", symbol, repo, err)
	}
	sym := resp.GetSymbol()
	if sym == nil || sym.GetName() == "" {
		return tools.SymbolInfo{}, fmt.Errorf("no symbol named %q is indexed in %s", symbol, repo)
	}
	return tools.SymbolInfo{
		Name: sym.GetName(),
		Kind: sym.GetKind(),
		Path: sym.GetPath(),
		Line: int(sym.GetStartLine()),
	}, nil
}

// GetDependencies answers what the symbol or file at path depends on, from
// the graph's dependency edges.
func (a *gitAdapter) GetDependencies(ctx context.Context, repo, ref, path string) ([]string, error) {
	if a.graph == nil {
		return nil, fmt.Errorf("repo.get_dependencies needs the engineering-graph service, which this deployment has not configured")
	}
	// The tool takes "a file path or symbol name", and the symbol query
	// resolves only names: a path used to answer NotFound for every file.
	if strings.Contains(path, "/") || strings.Contains(path, ".") {
		rel, err := a.graph.FileRelations(ctx, &graphv1.FileRelationsRequest{RepoId: repo, Path: path})
		if err != nil {
			return nil, fmt.Errorf("dependencies of file %s in %s: %w", path, repo, err)
		}
		deps := make([]string, 0, len(rel.GetImports()))
		for _, n := range rel.GetImports() {
			deps = append(deps, "imports "+n.GetAttrs()["import_path"])
		}
		return deps, nil
	}
	resp, err := a.graph.Dependencies(ctx, &graphv1.DependenciesRequest{RepoId: repo, Symbol: path})
	if err != nil {
		return nil, fmt.Errorf("dependencies of %s in %s: %w", path, repo, err)
	}
	deps := make([]string, 0, len(resp.GetNodes()))
	for _, n := range resp.GetNodes() {
		// A node's key is an internal identifier; a reader wants where the
		// dependency is.
		label := n.GetAttrs()["path"]
		if name := n.GetAttrs()["name"]; name != "" {
			label += "#" + name
		}
		if label == "" {
			label = n.GetKey()
		}
		deps = append(deps, label)
	}
	return deps, nil
}

func (a *gitAdapter) Diff(ctx context.Context, repo, from, to string) (string, error) {
	resp, err := a.git.GetDiff(ctx, &gitv1.GetDiffRequest{Repo: repo, From: from, To: to})
	if err != nil {
		return "", fmt.Errorf("diff %s %s..%s: %w", repo, from, to, err)
	}
	return resp.GetUnified(), nil
}

// Commit writes files onto branch through git-platform. An agent commits
// this way rather than with a git credential of its own: the platform, not
// the model, decides what may be written and to where.
func (a *gitAdapter) Commit(ctx context.Context, repo, branch, message string, files map[string]string) (string, error) {
	if len(files) == 0 {
		return "", fmt.Errorf("git.commit needs at least one file")
	}
	// Map iteration order is random; a commit's file list is sorted so the
	// same change produces the same request twice running.
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	changes := make([]*gitv1.FileChange, 0, len(paths))
	for _, p := range paths {
		changes = append(changes, &gitv1.FileChange{Path: p, Content: []byte(files[p])})
	}
	resp, err := a.git.CreateCommit(ctx, &gitv1.CreateCommitRequest{
		Repo:    repo,
		Branch:  branch,
		Message: message,
		Files:   changes,
	})
	if err != nil {
		return "", fmt.Errorf("commit to %s on %s: %w", repo, branch, err)
	}
	return resp.GetSha(), nil
}

// graphAdapter satisfies tools.GraphClient over the engineering-graph gRPC
// client, so architecture.query answers from the graph rather than
// reporting itself unconfigured.
// repoID is carried rather than taken per call because tools.GraphClient's
// Query has no repository parameter, and the graph scopes knowledge per
// repository: without it every architecture.query was refused for an empty
// repo_id.
type graphAdapter struct {
	graph  graphv1.GraphServiceClient
	repoID string
}

func newGraphAdapter(graph graphv1.GraphServiceClient, repoID string) tools.GraphClient {
	if graph == nil {
		return nil
	}
	return &graphAdapter{graph: graph, repoID: repoID}
}

func (a *graphAdapter) Query(ctx context.Context, query string) ([]string, error) {
	resp, err := a.graph.SearchKnowledge(ctx, &graphv1.SearchKnowledgeRequest{
		RepoId: a.repoID, Query: query, K: searchHitLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("architecture query %q: %w", query, err)
	}
	out := make([]string, 0, len(resp.GetEntries()))
	for _, e := range resp.GetEntries() {
		out = append(out, e.GetTitle()+": "+e.GetBody())
	}
	return out, nil
}

// reviewsAdapter satisfies tools.ReviewsClient over the reviews gRPC
// client. Per-gate proof lives on an Engineering Run, so gate.status is
// answered from there rather than from the gates service directly.
type reviewsAdapter struct {
	reviews reviewsv1.ReviewsServiceClient
}

func newReviewsAdapter(reviews reviewsv1.ReviewsServiceClient) tools.ReviewsClient {
	if reviews == nil {
		return nil
	}
	return &reviewsAdapter{reviews: reviews}
}

func (a *reviewsAdapter) GateStatus(ctx context.Context, runID string) (map[string]string, error) {
	resp, err := a.reviews.ListProof(ctx, &reviewsv1.ListProofRequest{RunId: runID})
	if err != nil {
		return nil, fmt.Errorf("gate status for run %s: %w", runID, err)
	}
	out := make(map[string]string, len(resp.GetProof()))
	for _, p := range resp.GetProof() {
		out[p.GetGate()] = p.GetStatus()
	}
	return out, nil
}

// ciAdapter satisfies tools.CIClient over the ci-runner gRPC client, so
// ci.get_logs reads a real job's log instead of reporting itself
// unconfigured.
type ciAdapter struct {
	ci civ1.CIServiceClient
}

func newCIAdapter(ci civ1.CIServiceClient) tools.CIClient {
	if ci == nil {
		return nil
	}
	return &ciAdapter{ci: ci}
}

// RunTest schedules a CI run for ref and returns its id. The workflow the
// repository declares is what runs; suite is not a separate dispatch
// mechanism, so a caller naming one is told plainly rather than having it
// silently ignored.
func (a *ciAdapter) RunTest(ctx context.Context, repo, ref, suite string) (string, error) {
	if suite != "" {
		return "", fmt.Errorf("ci.run_test runs the repository's declared workflow; it cannot run an arbitrary suite %q", suite)
	}
	resp, err := a.ci.TriggerRun(ctx, &civ1.TriggerRunRequest{RepoId: repo, Ref: ref})
	if err != nil {
		return "", fmt.Errorf("run tests for %s at %s: %w", repo, ref, err)
	}
	return resp.GetRun().GetId(), nil
}

func (a *ciAdapter) GetLogs(ctx context.Context, jobID string) (string, error) {
	resp, err := a.ci.GetJobLogs(ctx, &civ1.GetJobLogsRequest{JobId: jobID})
	if err != nil {
		return "", fmt.Errorf("read log for job %s: %w", jobID, err)
	}
	return strings.Join(resp.GetLines(), "\n"), nil
}
