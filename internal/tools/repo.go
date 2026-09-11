package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// SearchHit is one match returned by repo.search.
type SearchHit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// SymbolInfo is the definition site returned by repo.get_symbol.
type SymbolInfo struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

type repoSearchArgs struct {
	Repo  string `json:"repo"`
	Query string `json:"query"`
}

type repoSearchResult struct {
	Hits []SearchHit `json:"hits"`
}

type repoReadFileArgs struct {
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
	Path string `json:"path"`
}

type repoReadFileResult struct {
	Content string `json:"content"`
}

type repoGetSymbolArgs struct {
	Repo   string `json:"repo"`
	Ref    string `json:"ref"`
	Symbol string `json:"symbol"`
}

type repoGetDependenciesArgs struct {
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
	Path string `json:"path"`
}

type repoGetDependenciesResult struct {
	Dependencies []string `json:"dependencies"`
}

type architectureQueryArgs struct {
	Query string `json:"query"`
}

type architectureQueryResult struct {
	Results []string `json:"results"`
}

func registerRepoTools(r *Registry) {
	r.Register("repo.search", repoSearchHandler)
	r.Register("repo.read_file", repoReadFileHandler)
	r.Register("repo.get_symbol", repoGetSymbolHandler)
	r.Register("repo.get_dependencies", repoGetDependenciesHandler)
	r.Register("architecture.query", architectureQueryHandler)
}

func repoSearchHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args repoSearchArgs
	if err := unmarshalArgs("repo.search", argsJSON, &args); err != nil {
		return nil, err
	}
	if rt.Git == nil {
		return nil, fmt.Errorf("repo.search: no git client configured")
	}
	hits, err := rt.Git.Search(ctx, args.Repo, args.Query)
	if err != nil {
		return nil, fmt.Errorf("repo.search: %w", err)
	}
	return json.Marshal(repoSearchResult{Hits: hits})
}

func repoReadFileHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args repoReadFileArgs
	if err := unmarshalArgs("repo.read_file", argsJSON, &args); err != nil {
		return nil, err
	}
	if rt.Git == nil {
		return nil, fmt.Errorf("repo.read_file: no git client configured")
	}
	content, err := rt.Git.ReadFile(ctx, args.Repo, args.Ref, args.Path)
	if err != nil {
		return nil, fmt.Errorf("repo.read_file: %w", err)
	}
	return json.Marshal(repoReadFileResult{Content: string(content)})
}

func repoGetSymbolHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args repoGetSymbolArgs
	if err := unmarshalArgs("repo.get_symbol", argsJSON, &args); err != nil {
		return nil, err
	}
	if rt.Git == nil {
		return nil, fmt.Errorf("repo.get_symbol: no git client configured")
	}
	sym, err := rt.Git.GetSymbol(ctx, args.Repo, args.Ref, args.Symbol)
	if err != nil {
		return nil, fmt.Errorf("repo.get_symbol: %w", err)
	}
	return json.Marshal(sym)
}

func repoGetDependenciesHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args repoGetDependenciesArgs
	if err := unmarshalArgs("repo.get_dependencies", argsJSON, &args); err != nil {
		return nil, err
	}
	if rt.Git == nil {
		return nil, fmt.Errorf("repo.get_dependencies: no git client configured")
	}
	deps, err := rt.Git.GetDependencies(ctx, args.Repo, args.Ref, args.Path)
	if err != nil {
		return nil, fmt.Errorf("repo.get_dependencies: %w", err)
	}
	return json.Marshal(repoGetDependenciesResult{Dependencies: deps})
}

func architectureQueryHandler(ctx context.Context, rt Runtime, argsJSON []byte) ([]byte, error) {
	var args architectureQueryArgs
	if err := unmarshalArgs("architecture.query", argsJSON, &args); err != nil {
		return nil, err
	}
	if rt.Graph == nil {
		return nil, fmt.Errorf("architecture.query: no graph client configured")
	}
	results, err := rt.Graph.Query(ctx, args.Query)
	if err != nil {
		return nil, fmt.Errorf("architecture.query: %w", err)
	}
	return json.Marshal(architectureQueryResult{Results: results})
}
